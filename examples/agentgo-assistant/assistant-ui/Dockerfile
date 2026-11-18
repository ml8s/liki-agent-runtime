FROM caddy:2.10.2-alpine@sha256:4c6e91c6ed0e2fa03efd5b44747b625fec79bc9cd06ac5235a779726618e530d

COPY Caddyfile /etc/caddy/Caddyfile
COPY index.html /srv/index.html
COPY app.js /srv/app.js
COPY vendor /srv/vendor/

EXPOSE 8080
