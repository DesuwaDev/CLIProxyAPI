package wire

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/hpack"
)

// HTTP/2 connection preface values observed from codex-cli 0.154.0 (hyper h2).
const (
	settingInitialWindowSize = 2097152
	settingMaxFrameSize      = 16384
	settingMaxHeaderListSize = 16384
	connectionWindowUpdate   = 5177345
	defaultPeerWindow        = 65535
	defaultPeerMaxFrameSize  = 16384
	streamWindowUpdateAt     = settingInitialWindowSize / 2
	connWindowUpdateAt       = (defaultPeerWindow + connectionWindowUpdate) / 2
	idleTimeout              = 90 * time.Second // reqwest pool_idle_timeout default
)

var (
	errConnClosed   = errors.New("wire: http2 connection closed")
	errGoAway       = errors.New("wire: http2 connection received GOAWAY")
	errStreamsMaxed = errors.New("wire: http2 connection has no free streams")
	errStreamDone   = errors.New("wire: http2 stream finished")
)

// h2Conn is a minimal HTTP/2 client connection whose preface, SETTINGS and
// header encoding follow the captured profile. It multiplexes streams and
// honours peer flow control; it does not implement server push or priorities.
type h2Conn struct {
	conn net.Conn
	fr   *http2.Framer
	henc *h2HpackEncoder
	hbuf []byte

	openMu sync.Mutex // preserves stream ID order through the first HEADERS write
	wmu    sync.Mutex // serializes frame writes

	mu           sync.Mutex
	nextStreamID uint32
	streams      map[uint32]*h2Stream
	closed       bool
	closeErr     error
	goAwayLast   uint32
	goAway       bool
	draining     bool
	lastActive   time.Time

	peerMaxFrameSize           uint32
	peerInitialWindow          int64
	peerMaxStreams             uint32
	peerHeaderTableSize        uint32
	peerHeaderTableSizeChanged bool
	connSendWindow             int64
	windowNotify               chan struct{}

	connRecvConsumed int64
	done             chan struct{}
}

type h2Stream struct {
	id   uint32
	conn *h2Conn

	sendWindow int64
	sendEnded  bool // guarded by conn.mu

	mu       sync.Mutex
	cond     *sync.Cond
	buf      bytes.Buffer
	consumed int64
	ended    bool
	err      error
	respCh   chan *http.Response
	respDone bool
	closed   bool
	done     chan struct{}
}

// newH2Conn takes an already negotiated (ALPN h2) TLS connection, sends the
// client preface and starts the read loop.
func newH2Conn(conn net.Conn) (*h2Conn, error) {
	c := &h2Conn{
		conn:              conn,
		nextStreamID:      1,
		streams:           map[uint32]*h2Stream{},
		peerMaxFrameSize:  defaultPeerMaxFrameSize,
		peerInitialWindow: defaultPeerWindow,
		peerMaxStreams:    100,
		connSendWindow:    defaultPeerWindow,
		windowNotify:      make(chan struct{}),
		done:              make(chan struct{}),
		lastActive:        time.Now(),
	}
	c.fr = http2.NewFramer(conn, conn)
	c.fr.SetMaxReadFrameSize(settingMaxFrameSize)
	c.fr.ReadMetaHeaders = hpack.NewDecoder(4096, nil)
	c.fr.MaxHeaderListSize = settingMaxHeaderListSize
	c.henc = newH2HpackEncoder()

	c.wmu.Lock()
	go c.readLoop()
	err := c.writePreface()
	c.wmu.Unlock()
	if err != nil {
		c.closeWith(err)
		return nil, err
	}
	return c, nil
}

func (c *h2Conn) writePreface() error {
	if _, err := io.WriteString(c.conn, http2.ClientPreface); err != nil {
		return fmt.Errorf("wire: write http2 preface: %w", err)
	}
	if err := c.fr.WriteSettings(
		http2.Setting{ID: http2.SettingEnablePush, Val: 0},
		http2.Setting{ID: http2.SettingInitialWindowSize, Val: settingInitialWindowSize},
		http2.Setting{ID: http2.SettingMaxFrameSize, Val: settingMaxFrameSize},
		http2.Setting{ID: http2.SettingMaxHeaderListSize, Val: settingMaxHeaderListSize},
	); err != nil {
		return fmt.Errorf("wire: write http2 settings: %w", err)
	}
	if err := c.fr.WriteWindowUpdate(0, connectionWindowUpdate); err != nil {
		return fmt.Errorf("wire: write http2 window update: %w", err)
	}
	return nil
}

// canTakeNewRequest reports whether a new stream may be opened.
func (c *h2Conn) canTakeNewRequest() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.goAway || c.draining {
		return false
	}
	if c.nextStreamID > 1<<30 {
		return false
	}
	return uint32(len(c.streams)) < c.peerMaxStreams
}

func (c *h2Conn) idleSince() (time.Time, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastActive, len(c.streams) == 0
}

// roundTrip sends req (body fully buffered by the caller) and returns the
// response once its HEADERS arrive. The body streams as DATA frames arrive.
func (c *h2Conn) roundTrip(req *http.Request, body []byte, fields []headerField) (resp *http.Response, err error) {
	c.openMu.Lock()
	if err := req.Context().Err(); err != nil {
		c.openMu.Unlock()
		return nil, err
	}
	c.mu.Lock()
	if c.closed || c.goAway || c.draining {
		c.mu.Unlock()
		c.openMu.Unlock()
		return nil, errConnClosed
	}
	if uint32(len(c.streams)) >= c.peerMaxStreams {
		c.mu.Unlock()
		c.openMu.Unlock()
		return nil, errStreamsMaxed
	}
	st := &h2Stream{id: c.nextStreamID, conn: c, sendWindow: c.peerInitialWindow, sendEnded: len(body) == 0, respCh: make(chan *http.Response, 1), done: make(chan struct{})}
	st.cond = sync.NewCond(&st.mu)
	c.nextStreamID += 2
	c.streams[st.id] = st
	c.lastActive = time.Now()
	c.mu.Unlock()
	defer func() {
		if err != nil {
			_ = (&streamBody{st: st}).Close()
		}
	}()

	authority := req.URL.Host
	if host, port, err := net.SplitHostPort(authority); err == nil && port == "443" {
		authority = host
	}
	endStream := len(body) == 0
	err = c.writeHeaders(st.id, req.Method, authority, req.URL.RequestURI(), fields, endStream)
	c.openMu.Unlock()
	if err != nil {
		c.closeWith(err)
		return nil, err
	}
	ctx := req.Context()
	go st.watchCancel(ctx)
	if !endStream {
		// A peer can return a final response before accepting the whole upload.
		// Keep reading that response while the writer observes stream termination.
		go func() {
			if errWrite := c.writeBody(ctx, st, body); errWrite != nil && !errors.Is(errWrite, errStreamDone) {
				st.fail(errWrite)
				c.resetStream(st, http2.ErrCodeCancel)
			}
		}()
	}
	select {
	case resp := <-st.respCh:
		if resp == nil {
			st.mu.Lock()
			err := st.err
			st.mu.Unlock()
			if err == nil {
				err = errConnClosed
			}
			return nil, err
		}
		resp.Request = req
		return resp, nil
	case <-ctx.Done():
		c.resetStream(st, http2.ErrCodeCancel)
		return nil, ctx.Err()
	}
}

func (c *h2Conn) writeHeaders(streamID uint32, method, authority, path string, fields []headerField, endStream bool) error {
	c.mu.Lock()
	maxFrame := int(c.peerMaxFrameSize)
	tableSize, tableSizeChanged := c.peerHeaderTableSize, c.peerHeaderTableSizeChanged
	c.peerHeaderTableSizeChanged = false
	c.mu.Unlock()
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if tableSizeChanged {
		c.henc.setMaxSize(tableSize)
	}
	// hyper pseudo-header order: :method :scheme :authority :path
	all := make([]hpack.HeaderField, 0, 4+len(fields))
	all = append(all,
		hpack.HeaderField{Name: ":method", Value: method},
		hpack.HeaderField{Name: ":scheme", Value: "https"},
		hpack.HeaderField{Name: ":authority", Value: authority},
		hpack.HeaderField{Name: ":path", Value: path},
	)
	for _, f := range fields {
		all = append(all, hpack.HeaderField{Name: f.name, Value: f.value})
	}
	c.hbuf = c.henc.encode(c.hbuf[:0], all)
	block := c.hbuf
	first := true
	for len(block) > 0 || first {
		chunk := block
		if len(chunk) > maxFrame {
			chunk = chunk[:maxFrame]
		}
		block = block[len(chunk):]
		last := len(block) == 0
		var err error
		if first {
			err = c.fr.WriteHeaders(http2.HeadersFrameParam{StreamID: streamID, BlockFragment: chunk, EndStream: endStream, EndHeaders: last})
			first = false
		} else {
			err = c.fr.WriteContinuation(streamID, last, chunk)
		}
		if err != nil {
			return fmt.Errorf("wire: write http2 headers: %w", err)
		}
	}
	return nil
}

func (c *h2Conn) writeBody(ctx context.Context, st *h2Stream, body []byte) error {
	for len(body) > 0 {
		n, err := c.reserveWindow(ctx, st, len(body))
		if err != nil {
			return err
		}
		chunk := body[:n]
		body = body[n:]
		c.wmu.Lock()
		select {
		case <-st.done:
			c.mu.Lock()
			c.connSendWindow += int64(n)
			c.signalWindow()
			c.mu.Unlock()
			c.wmu.Unlock()
			return errStreamDone
		default:
		}
		err = c.fr.WriteData(st.id, len(body) == 0, chunk)
		if err == nil && len(body) == 0 {
			c.mu.Lock()
			st.sendEnded = true
			c.mu.Unlock()
		}
		c.wmu.Unlock()
		if err != nil {
			c.closeWith(err)
			return fmt.Errorf("wire: write http2 data: %w", err)
		}
	}
	return nil
}

// reserveWindow blocks until the stream and connection send windows allow at
// least one byte, then reserves up to want bytes bounded by the peer frame size.
func (c *h2Conn) reserveWindow(ctx context.Context, st *h2Stream, want int) (int, error) {
	for {
		select {
		case <-st.done:
			return 0, errStreamDone
		case <-ctx.Done():
			return 0, ctx.Err()
		default:
		}
		c.mu.Lock()
		if c.closed {
			err := c.closeErr
			c.mu.Unlock()
			if err == nil {
				err = errConnClosed
			}
			return 0, err
		}
		n := int64(want)
		if n > int64(c.peerMaxFrameSize) {
			n = int64(c.peerMaxFrameSize)
		}
		if n > st.sendWindow {
			n = st.sendWindow
		}
		if n > c.connSendWindow {
			n = c.connSendWindow
		}
		if n > 0 {
			st.sendWindow -= n
			c.connSendWindow -= n
			c.mu.Unlock()
			return int(n), nil
		}
		notify := c.windowNotify
		c.mu.Unlock()
		select {
		case <-notify:
		case <-st.done:
			return 0, errStreamDone
		case <-ctx.Done():
			return 0, ctx.Err()
		case <-c.done:
			return 0, errConnClosed
		}
	}
}

func (c *h2Conn) signalWindow() {
	close(c.windowNotify)
	c.windowNotify = make(chan struct{})
}

func (c *h2Conn) removeStream(id uint32) {
	c.mu.Lock()
	delete(c.streams, id)
	c.lastActive = time.Now()
	closeIdle := c.draining && len(c.streams) == 0
	c.mu.Unlock()
	if closeIdle {
		c.closeWith(errConnClosed)
	}
}

func (c *h2Conn) resetStream(st *h2Stream, code http2.ErrCode) {
	st.fail(errors.New("wire: stream reset"))
	c.mu.Lock()
	_, present := c.streams[st.id]
	st.sendEnded = true
	closed := c.closed
	c.mu.Unlock()
	if present && !closed {
		c.wmu.Lock()
		_ = c.fr.WriteRSTStream(st.id, code)
		c.wmu.Unlock()
	}
	c.removeStream(st.id)
}

// finishStream preserves a complete response while stopping any unfinished upload.
func (c *h2Conn) finishStream(st *h2Stream) {
	st.finish()
	c.wmu.Lock()
	c.mu.Lock()
	resetUpload := !st.sendEnded && !c.closed
	st.sendEnded = true
	c.mu.Unlock()
	if resetUpload {
		_ = c.fr.WriteRSTStream(st.id, http2.ErrCodeNo)
	}
	c.wmu.Unlock()
	c.removeStream(st.id)
}

func (c *h2Conn) readLoop() {
	var err error
	defer func() {
		c.closeWith(err)
	}()
	for {
		frame, errRead := c.fr.ReadFrame()
		if errRead != nil {
			err = errRead
			return
		}
		switch f := frame.(type) {
		case *http2.SettingsFrame:
			if f.IsAck() {
				continue
			}
			c.applySettings(f)
			c.wmu.Lock()
			errAck := c.fr.WriteSettingsAck()
			c.wmu.Unlock()
			if errAck != nil {
				err = errAck
				return
			}
		case *http2.PingFrame:
			if f.IsAck() {
				continue
			}
			c.wmu.Lock()
			errPing := c.fr.WritePing(true, f.Data)
			c.wmu.Unlock()
			if errPing != nil {
				err = errPing
				return
			}
		case *http2.WindowUpdateFrame:
			c.mu.Lock()
			if f.StreamID == 0 {
				c.connSendWindow += int64(f.Increment)
			} else if st := c.streams[f.StreamID]; st != nil {
				st.sendWindow += int64(f.Increment)
			}
			c.signalWindow()
			c.mu.Unlock()
		case *http2.GoAwayFrame:
			c.mu.Lock()
			c.goAway = true
			c.goAwayLast = f.LastStreamID
			var pending []*h2Stream
			for id, st := range c.streams {
				if id > f.LastStreamID {
					pending = append(pending, st)
					delete(c.streams, id)
				}
			}
			c.mu.Unlock()
			for _, st := range pending {
				st.fail(errGoAway)
			}
			if f.ErrCode != http2.ErrCodeNo {
				err = fmt.Errorf("wire: http2 GOAWAY %v", f.ErrCode)
				return
			}
		case *http2.RSTStreamFrame:
			if st := c.lookup(f.StreamID); st != nil {
				c.removeStream(f.StreamID)
				st.fail(fmt.Errorf("wire: http2 stream reset by peer: %v", f.ErrCode))
			}
		case *http2.MetaHeadersFrame:
			st := c.lookup(f.StreamID)
			if st == nil {
				continue
			}
			st.deliverHeaders(f)
			if f.StreamEnded() {
				c.finishStream(st)
			}
		case *http2.DataFrame:
			st := c.lookup(f.StreamID)
			data := f.Data()
			if st == nil {
				// Stream already cancelled locally; keep the connection window healthy.
				c.consumed(nil, len(data)+padLen(f))
				continue
			}
			st.deliverData(data, padLen(f))
			if f.StreamEnded() {
				c.finishStream(st)
			}
		default:
			// PRIORITY, PUSH_PROMISE (disabled) and unknown frames are ignored.
		}
	}
}

func padLen(f *http2.DataFrame) int {
	if !f.Header().Flags.Has(http2.FlagDataPadded) {
		return 0
	}
	return int(f.Header().Length) - len(f.Data())
}

func (c *h2Conn) applySettings(f *http2.SettingsFrame) {
	c.mu.Lock()
	defer c.mu.Unlock()
	_ = f.ForeachSetting(func(s http2.Setting) error {
		switch s.ID {
		case http2.SettingMaxFrameSize:
			if s.Val >= 16384 && s.Val <= 1<<24-1 {
				c.peerMaxFrameSize = s.Val
			}
		case http2.SettingInitialWindowSize:
			delta := int64(s.Val) - c.peerInitialWindow
			c.peerInitialWindow = int64(s.Val)
			for _, st := range c.streams {
				st.sendWindow += delta
			}
		case http2.SettingMaxConcurrentStreams:
			if s.Val > 0 {
				c.peerMaxStreams = s.Val
			}
		case http2.SettingHeaderTableSize:
			c.peerHeaderTableSize = s.Val
			c.peerHeaderTableSizeChanged = true
		}
		return nil
	})
	c.signalWindow()
}

func (c *h2Conn) lookup(id uint32) *h2Stream {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.streams[id]
}

func (c *h2Conn) closeWith(err error) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	c.closed = true
	if err == nil {
		err = errConnClosed
	}
	c.closeErr = err
	streams := make([]*h2Stream, 0, len(c.streams))
	for _, st := range c.streams {
		streams = append(streams, st)
	}
	c.streams = map[uint32]*h2Stream{}
	close(c.done)
	c.signalWindow()
	c.mu.Unlock()
	_ = c.conn.Close()
	for _, st := range streams {
		st.fail(err)
	}
}

// Close shuts the connection down; in-flight streams fail with errConnClosed.
func (c *h2Conn) Close() error {
	// Closing the socket also unblocks any pending frame write during shutdown.
	c.closeWith(errConnClosed)
	return nil
}

// drain retires a connection without interrupting streams already using it.
func (c *h2Conn) drain() {
	c.mu.Lock()
	c.draining = true
	idle := len(c.streams) == 0
	c.mu.Unlock()
	if idle {
		c.closeWith(errConnClosed)
	}
}

func (c *h2Conn) isClosed() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.closed
}

// consumed reports body bytes read by the consumer so WINDOW_UPDATE frames can
// be emitted for the stream and the connection.
func (c *h2Conn) consumed(st *h2Stream, n int) {
	if n <= 0 {
		return
	}
	var connUpdate, streamUpdate uint32
	c.mu.Lock()
	c.connRecvConsumed += int64(n)
	if c.connRecvConsumed >= connWindowUpdateAt {
		connUpdate = uint32(c.connRecvConsumed)
		c.connRecvConsumed = 0
	}
	active := false
	if st != nil {
		_, active = c.streams[st.id]
	}
	closed := c.closed
	c.mu.Unlock()
	if active {
		st.mu.Lock()
		if !st.closed && !st.ended && st.err == nil {
			st.consumed += int64(n)
			if st.consumed >= streamWindowUpdateAt {
				streamUpdate = uint32(st.consumed)
				st.consumed = 0
			}
		}
		st.mu.Unlock()
	}
	if closed || (connUpdate == 0 && streamUpdate == 0) {
		return
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if connUpdate > 0 {
		_ = c.fr.WriteWindowUpdate(0, connUpdate)
	}
	if streamUpdate > 0 && active {
		_ = c.fr.WriteWindowUpdate(st.id, streamUpdate)
	}
}

func (st *h2Stream) deliverHeaders(f *http2.MetaHeadersFrame) {
	status, _ := strconv.Atoi(f.PseudoValue("status"))
	if status >= 100 && status < 200 {
		return
	}
	st.mu.Lock()
	if st.respDone {
		// Trailers: nothing to surface for this client.
		st.mu.Unlock()
		return
	}
	st.respDone = true
	st.mu.Unlock()
	header := http.Header{}
	for _, hf := range f.RegularFields() {
		header.Add(http.CanonicalHeaderKey(hf.Name), hf.Value)
	}
	resp := &http.Response{
		Status:     fmt.Sprintf("%d %s", status, http.StatusText(status)),
		StatusCode: status,
		Proto:      "HTTP/2.0",
		ProtoMajor: 2,
		Header:     header,
		Body:       &streamBody{st: st},
	}
	resp.ContentLength = -1
	if cl := header.Get("Content-Length"); cl != "" {
		if n, err := strconv.ParseInt(cl, 10, 64); err == nil {
			resp.ContentLength = n
		}
	}
	if f.StreamEnded() {
		resp.Body = http.NoBody
	}
	st.respCh <- resp
}

func (st *h2Stream) deliverData(data []byte, padding int) {
	st.mu.Lock()
	discard := st.ended || st.closed || st.err != nil
	if !discard {
		st.buf.Write(data)
	}
	st.cond.Broadcast()
	st.mu.Unlock()
	if discard {
		st.conn.consumed(nil, len(data)+padding)
		return
	}
	if padding > 0 {
		st.conn.consumed(st, padding)
	}
}

func (st *h2Stream) finish() {
	st.mu.Lock()
	st.ended = true
	st.stopLocked()
	respPending := !st.respDone
	st.cond.Broadcast()
	st.mu.Unlock()
	if respPending {
		st.fail(errors.New("wire: http2 stream ended before response headers"))
	}
}

func (st *h2Stream) fail(err error) {
	st.mu.Lock()
	if st.err == nil {
		st.err = err
	}
	respPending := !st.respDone
	st.respDone = true
	st.stopLocked()
	st.cond.Broadcast()
	st.mu.Unlock()
	if respPending {
		select {
		case st.respCh <- nil:
		default:
		}
	}
}

func (st *h2Stream) stopLocked() {
	if st.done != nil {
		select {
		case <-st.done:
		default:
			close(st.done)
		}
	}
}

// watchCancel resets the stream when the request context ends before EOF.
func (st *h2Stream) watchCancel(ctx context.Context) {
	if ctx == nil {
		return
	}
	select {
	case <-ctx.Done():
		_ = (&streamBody{st: st}).Close()
	case <-st.done:
	case <-st.conn.done:
	}
}

type streamBody struct{ st *h2Stream }

func (b *streamBody) Read(p []byte) (int, error) {
	st := b.st
	st.mu.Lock()
	for st.buf.Len() == 0 && !st.ended && st.err == nil && !st.closed {
		st.cond.Wait()
	}
	if st.buf.Len() > 0 {
		n, _ := st.buf.Read(p)
		st.mu.Unlock()
		st.conn.consumed(st, n)
		return n, nil
	}
	err := st.err
	closed := st.closed
	ended := st.ended
	st.mu.Unlock()
	switch {
	case ended && err == nil:
		return 0, io.EOF
	case closed && err == nil:
		return 0, errors.New("wire: read on closed response body")
	default:
		return 0, err
	}
}

func (b *streamBody) Close() error {
	st := b.st
	st.mu.Lock()
	if st.closed {
		st.mu.Unlock()
		return nil
	}
	st.closed = true
	unread := st.buf.Len()
	st.buf.Reset()
	st.stopLocked()
	needReset := !st.ended && st.err == nil
	st.cond.Broadcast()
	st.mu.Unlock()
	if needReset {
		st.conn.resetStream(st, http2.ErrCodeCancel)
	}
	st.conn.consumed(nil, unread)
	return nil
}

// shouldCompress reports whether the profile compresses this request body.
// The CLI compresses the Responses API POST body; other calls are sent as-is.
func shouldCompress(req *http.Request, body []byte) bool {
	if req.Method != http.MethodPost || len(body) == 0 {
		return false
	}
	if req.Header.Get("Content-Encoding") != "" {
		return false
	}
	if !strings.HasPrefix(strings.ToLower(req.Header.Get("Content-Type")), "application/json") {
		return false
	}
	return strings.HasSuffix(req.URL.Path, "/responses")
}
