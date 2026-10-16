# cannon-ext-backup-gdrive

Cannon extension that backs up `data_root` and `sites.json` to [Google Drive](https://rclone.org/drive/).

OAuth setup follows the same model as [rclone](https://github.com/rclone/rclone): your own Google Cloud client ID/secret, browser consent, offline refresh token, and Drive API uploads.

## Features

- **Backup Log** admin panel — history table + **Backup Now**
- **Setup** admin panel — OAuth credentials, Connect Google Drive, folder picker
- **Schedule** configuration — cron `frequency`, enable flag, retain count

## Install

1. Build and place the binary in your Cannon `extensions.dir`:

   ```bash
   go build -o cannon-ext-backup-gdrive .
   cp cannon-ext-backup-gdrive /path/to/extensions/
   ```

2. Enable the extension for a site in Cannon admin.
3. Open **Extension Apps → Drive Backup → Setup**.
4. Create a Google OAuth **Web application** client ([rclone client ID guide](https://rclone.org/drive/#making-your-own-client-id)).
5. Add the redirect URI shown on Setup. The path is always
   `/admin/extension-apps/cannon-ext-backup-gdrive/oauth/callback`
   (host comes from the admin request — Host / `X-Forwarded-*`).
6. Connect Drive, select a folder, then set **Schedule** cron under Configuration.

## Archive contents

```
sites.json
data_root/...
```

The runtime `sockets` directory under `data_root` is skipped.

## Configuration

| Field | Description |
|-------|-------------|
| `enabled` | Run backups on the cron schedule |
| `frequency` | 5-field cron or descriptor (`0 2 * * *`, `@daily`) |
| `retain_count` | Keep N newest `cannon-backup-*` files in Drive (`0` = keep all) |

## Notes

- Backups are app-wide (`data_root` + loaded `sites.json`). Enable this extension on one management site.
- Scope used: `https://www.googleapis.com/auth/drive` (needed to browse existing folders), matching rclone’s default `drive` scope.
