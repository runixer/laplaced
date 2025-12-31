# Security Policy

## Reporting a Vulnerability

If you discover a security vulnerability, please report it privately:

1. **Do NOT** open a public issue
2. Email: [create an issue with "Security" label](https://github.com/runixer/laplaced/issues/new)
3. Include steps to reproduce

I'll respond as soon as possible.

## Supported Versions

| Version | Supported |
|---------|-----------|
| latest  | ✅        |

## Security Best Practices

When deploying this bot:

- Keep your `.env` file secure and never commit it
- Use strong passwords for the web dashboard
- Restrict `LAPLACED_ALLOWED_USER_IDS` to trusted users only
- Run behind a reverse proxy with HTTPS for webhooks
