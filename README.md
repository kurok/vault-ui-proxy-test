# vault-ui-proxy-test

A local proof-of-concept that places NGINX in front of HashiCorp Vault and visually differentiates the Vault UI by injecting a CSS override — without modifying the Vault binary or image.

## What it does

- Runs HashiCorp Vault in dev mode with the UI enabled.
- Places NGINX as a reverse proxy in front of Vault.
- Injects a small CSS file into every `/ui/` HTML response via NGINX `sub_filter`.
- The injected CSS adds an animated environment banner, changes sidebar colors, overrides the Vault brand accent, and replaces the sidebar logo text.
- All Vault API paths (`/v1/*`) pass through NGINX unchanged.

```
Browser → http://localhost:8080
              │
              ▼
           NGINX :80
           ├── /ui/*    → proxy_pass vault:8200  +  sub_filter injects <link> to /_env/override.css
           ├── /_env/*  → static CSS served directly by NGINX
           └── /*       → proxy_pass vault:8200  (no rewrite)
              │
              ▼
        Vault dev :8200
```

## Requirements

- [Docker](https://docs.docker.com/get-docker/) with Compose v2 (`docker compose` command)
- Ports 8080 and 8200 free on localhost

## Quick start

```bash
git clone https://github.com/kurok/vault-ui-proxy-test.git
cd vault-ui-proxy-test
docker compose up -d
```

Wait ~5 seconds for Vault to initialize (NGINX waits for Vault's healthcheck automatically).

## Access

| URL | Description |
|-----|-------------|
| http://localhost:8080/ui/ | Vault UI via NGINX — shows injected CSS overrides |
| http://localhost:8200/ui/ | Vault UI direct — no overrides |

**Vault root token:** `root`

## Validate

Run the included validation script:

```bash
./scripts/validate.sh
```

Or manually:

```bash
# Vault health via proxy
curl -s http://localhost:8080/v1/sys/health | python3 -m json.tool

# Confirm override CSS is injected in proxied UI
curl -s http://localhost:8080/ui/ | grep -c 'override.css'
# Expected: 1

# Confirm override CSS is NOT in direct Vault UI
curl -s http://localhost:8200/ui/ | grep -c 'override.css'
# Expected: 0

# Confirm API response is not rewritten
curl -s http://localhost:8080/v1/sys/health | grep -c 'override.css'
# Expected: 0
```

## Customizing the override

Edit `nginx/static/env-override.css` — no container rebuild needed.

After editing, reload NGINX:

```bash
./scripts/reload-nginx.sh
```

### Key CSS hooks

| What to change | CSS target |
|----------------|------------|
| Banner text | `body::before { content: "..." }` |
| Banner background | `body::before { background: ... }` |
| Sidebar background | `--token-side-nav-color-surface-primary` |
| Sidebar hover color | `--token-side-nav-color-surface-interactive-hover` |
| Vault brand accent color | `--token-color-vault-brand` |
| Logo area text | `.hds-app-side-nav__header-home-link::after { content: "..." }` |

CSS custom properties (`--token-*`) come from the [HashiCorp Design System](https://helios.hashicorp.design/) and are stable across Vault minor versions.

### Example: change banner to staging orange

```css
body::before {
  content: "STAGING";
  background: #c05e00;
}
```

### Example: change banner to a static color instead of animated rainbow

```css
body::before {
  content: "LOCAL TEST";
  background: #b42318;
  animation: none;
}
```

## Stop

```bash
docker compose down
```

## Project structure

```
vault-ui-proxy-test/
├── docker-compose.yml          # Vault + NGINX stack
├── nginx/
│   ├── default.conf            # NGINX proxy config with sub_filter
│   └── static/
│       └── env-override.css    # Injected CSS overrides
├── scripts/
│   ├── start.sh                # Start the stack
│   ├── stop.sh                 # Stop the stack
│   ├── reload-nginx.sh         # Reload NGINX config without restart
│   └── validate.sh             # Run validation checklist
└── README.md
```

## How it works

### NGINX sub_filter

`ngx_http_sub_module` is compiled into `nginx:alpine` by default. The filter matches the literal string `</head>` in HTML responses and appends a `<link>` tag pointing to the override CSS before it:

```nginx
sub_filter '</head>' '<link rel="stylesheet" href="/_env/override.css"></head>';
```

`Accept-Encoding ""` is set on the upstream request to prevent Vault from gzip-compressing the HTML response, which would make the text substitution impossible.

### CSS custom properties

Vault UI uses the HashiCorp Design System (HDS), which exposes all colors as CSS custom properties on `:root`. Overriding these at the top of the injected stylesheet changes colors globally without touching any hashed class names.

### Logo replacement

The sidebar logo is rendered by JavaScript after page load. CSS targets the stable HDS class `.hds-app-side-nav__header-home-link`, hides the SVG child, and injects a text label via `::after`.

## Known limitations

- **Dev mode only.** Vault dev mode stores everything in memory; data is lost on container restart.
- **sub_filter and compression.** If Vault were ever to send pre-compressed responses that NGINX cannot decompress, the substitution would silently fail.
- **Logo is JS-rendered.** The logo override relies on a CSS pseudo-element; if HDS renames the class, the logo reverts to the default SVG.
- **CSP.** Vault sets a Content-Security-Policy header. The injected stylesheet loads from the same NGINX origin, so no CSP changes are required. External assets would require CSP header modification.
- **Not for production.** This is a local visual differentiation tool only.

## References

- [HashiCorp Vault UI configuration](https://developer.hashicorp.com/vault/docs/configuration/ui)
- [NGINX ngx_http_sub_module](https://nginx.org/en/docs/http/ngx_http_sub_module.html)
- [HashiCorp Design System tokens](https://helios.hashicorp.design/)
