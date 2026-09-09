# Putting the portal on the internet

The NUC sits behind campus NAT, so nothing can reach it from outside. A
Cloudflare Tunnel solves that without a public IP, an inbound firewall rule, or
a port forward: `cloudflared` makes an outbound connection to Cloudflare and
they proxy your hostname down it.

```bash
# on the NUC
curl -fsSL https://pkg.cloudflare.com/cloudflare-main.gpg \
  | sudo tee /usr/share/keyrings/cloudflare-main.gpg >/dev/null
echo "deb [signed-by=/usr/share/keyrings/cloudflare-main.gpg] https://pkg.cloudflare.com/cloudflared any main" \
  | sudo tee /etc/apt/sources.list.d/cloudflared.list
sudo apt update && sudo apt install -y cloudflared

cloudflared tunnel login                     # opens a browser link
cloudflared tunnel create bernard
cloudflared tunnel route dns bernard tv.startupshell.org
```

Then `/etc/cloudflared/config.yml`:

```yaml
tunnel: bernard
credentials-file: /root/.cloudflared/<TUNNEL-ID>.json

ingress:
  - hostname: tv.startupshell.org
    service: http://localhost:8080
    originRequest:
      # The display and the admin console hold an SSE stream open for as long
      # as the page is up. Without this Cloudflare closes it every 100 seconds
      # and the browser reconnects in a loop.
      disableChunkedEncoding: false
      connectTimeout: 30s
      noTLSVerify: false
  - service: http_status:404
```

```bash
sudo cloudflared service install
sudo systemctl enable --now cloudflared
```

Finally, in `/etc/bernard/bernard.env`:

```
BERNARD_PUBLIC_URL=https://tv.startupshell.org
BERNARD_TRUST_PROXY=true
```

and restart Bernard. `BERNARD_PUBLIC_URL` starting with `https://` is what
switches the session cookie to `Secure`.

Add `https://tv.startupshell.org` to the Authorized JavaScript origins on your
Google OAuth client, or sign-in will fail with `origin_mismatch`.

## Worth doing once it is public

- **Cloudflare Access** in front of `/admin` if you want a second gate on the
  review console beyond the admin email list.
- **A rate limit rule** on `POST /api/submissions`. Any signed-in member can
  upload 256 MB at a time; the NUC's disk is the limit.
- Leave port 8080 off the campus firewall's inbound rules. The tunnel is
  outbound-only and does not need it.
