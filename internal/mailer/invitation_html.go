package mailer

import "html/template"

// htmlTemplate is the branded invitation page in the Machinarchy palette
// (machine black, burnt steel, industrial orange, aged ivory). Styles are
// inline and fonts fall back to system ones, since mail clients load neither
// scripts nor web fonts. html/template escapes every value.
var htmlTemplate = template.Must(template.New("html").Parse(`<!doctype html>
<html lang="en"><body style="margin:0;padding:24px 12px;background:#111315;font-family:Inter,Arial,sans-serif;color:#D7CCB5">
<table role="presentation" width="100%" cellpadding="0" cellspacing="0"><tr><td align="center">
<table role="presentation" width="560" cellpadding="0" cellspacing="0" style="max-width:560px;width:100%;background:#1a1d1f;border:1px solid #414447;border-top:4px solid #E87519">
<tr><td style="padding:28px 32px 8px;font-family:Orbitron,'Exo 2',Rajdhani,Arial,sans-serif;font-size:20px;letter-spacing:4px;font-weight:bold;color:#E87519">MACHINARCHY</td></tr>
<tr><td style="padding:0 32px;font-size:12px;letter-spacing:1px;color:#873F27;text-transform:uppercase">Bringing order to machines, one host at a time</td></tr>
<tr><td style="padding:24px 32px 8px;font-size:16px;line-height:1.5">{{.Inviter}} invited you to Machinarchy as <strong>{{.Role}}</strong>.</td></tr>
<tr><td style="padding:16px 32px"><a href="{{.ServerURL}}" style="display:inline-block;background:#E87519;color:#111315;font-weight:bold;text-decoration:none;padding:14px 28px;letter-spacing:2px">OPEN MACHINARCHY</a></td></tr>
<tr><td style="padding:8px 32px;font-size:14px">Your one-time token, valid until {{.Expires}}:</td></tr>
<tr><td style="padding:0 32px 16px"><code style="display:block;background:#111315;border:1px solid #414447;padding:12px;font-family:'JetBrains Mono','IBM Plex Mono',Menlo,monospace;font-size:13px;word-break:break-all;color:#D7CCB5">{{.Token}}</code></td></tr>
<tr><td style="padding:0 32px 8px;font-size:14px">Then run:</td></tr>
<tr><td style="padding:0 32px 16px"><code style="display:block;background:#111315;border-left:3px solid #E87519;padding:12px;font-family:'JetBrains Mono','IBM Plex Mono',Menlo,monospace;font-size:13px;word-break:break-all;color:#E87519">{{.Command}}</code></td></tr>
<tr><td style="padding:0 32px 24px;font-size:12px;line-height:1.5;color:#8a8577">It asks for the token, then for the username and password you want. Nobody at Machinarchy will ask you for this token. If you did not expect this invitation, ignore the message.</td></tr>
</table></td></tr></table></body></html>`))
