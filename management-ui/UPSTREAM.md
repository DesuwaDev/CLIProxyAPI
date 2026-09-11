# Original management frontend

Source: https://github.com/router-for-me/Cli-Proxy-API-Management-Center
Pinned upstream commit: ed5f1c48e11ba7335f1e8f676f228c280196af85
The upstream MIT license is preserved in LICENSE.

This is the original React management panel, extended in place. There is one
/management.html, one hash router, one login, and the original navigation/theme.
Feature code lives in src/features/nativeManagement; the API adapter lives in
src/services/api/nativeManagement.ts and nativeInsights.ts and nativeControls.ts. Original entry-point changes are limited to
App.tsx (provider), MainRoutes.tsx (routes), MainLayout.tsx (navigation), and the
dashboard (persistent usage summary). Locale additions live under the native key.
Some older Russian explanatory copy uses English fallback text.

Build from the CPA root with scripts/build-native.ps1 (Node/npm, pinned Bun 1.3.14,
and Go required). The script installs with bun.lock frozen, builds the single-file
panel, copies it to internal/native/web/management.html, and compiles CPA. The HTML
bundle is checked in so ordinary go build needs no Node/Bun installation. A Go-only
build does not pick up frontend source edits until the bundle is regenerated.
Run npm exec --yes --package=bun@1.3.14 -- bun run verify here after frontend changes.

To update upstream, compare this directory with the pinned commit in a temporary
checkout, merge the new upstream files, and reapply the entry-point integrations
plus the native locale keys. Keep feature code separate from upstream files.
Update the pinned commit and build version, then verify and regenerate the bundle.
Do not replace this directory blindly: the enhanced panel is embedded in the same
CPA binary and updated with that binary, independently of the HTML auto-updater.

To remove a UI feature, remove its descriptor in registry.tsx and its page; keep
backend removal and database migrations separate. The native module master switch
in CPA configuration restores the upstream panel-serving path on the next restart.

Per-key controls are inserted in the original ApiKeysCardEditor, BaseProviderForm,
ApiKeyEntriesEditor and AuthFileDetailsSheet. Their shared implementation is
CredentialControls under the native feature. Native policy saves are explicitly
independent from credential/config saves. New unsaved upstream credentials must
be saved and reopened to obtain their stable auth index. Keep these four small
render integrations when merging upstream forms; no second settings panel exists.
The original price page embeds source presets and a per-model tier/context table.
