# Google Drive Backup

Backs up Cannon's `data_root` and `sites.json` to Google Drive.

## Setup

1. Open **Drive Backup → Setup**.
2. Create a Google Cloud OAuth **Web application** client. Follow rclone's guide: [Making your own client ID](https://rclone.org/drive/#making-your-own-client-id).
3. Add the redirect URI shown on the Setup page. Path is always
   `/admin/extension-apps/cannon-ext-backup-gdrive/oauth/callback`.
4. Save Client ID / Secret, click **Connect Google Drive**, then pick a destination folder.
5. Under **Schedule** (configuration), set a cron `frequency` (for example `0 2 * * *`) and enable scheduled backups.

## What is backed up

Each run creates `cannon-backup-YYYYMMDD-HHMMSS.tar.gz` containing:

- `sites.json` — the config file Cannon loaded (`CANNON_CONFIG`)
- `data_root/` — contents of `data_root` from sites.json (sockets directory skipped)

## Permissions

- `backup.view` — view log
- `backup.run` — run Backup Now
- `backup.setup` — configure OAuth / folder

## References

- [rclone Google Drive](https://rclone.org/drive/)
- [rclone source](https://github.com/rclone/rclone)
