# Vendored AG-UI client

This local bundle is copied from `examples/simple-chat/chat-ui/vendor/` so the
example needs no runtime CDN or frontend build step.

To regenerate it:

```bash
cd examples/simple-chat/chat-ui
npm ci
npm run build:vendor
cp vendor/ag-ui-client.js ../../agentgo-assistant/assistant-ui/vendor/ag-ui-client.js
```
