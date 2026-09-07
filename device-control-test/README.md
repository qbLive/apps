# Device Control Windows Test

This folder contains the Windows companion app for the XDreemB52 device-control page.

## Test build

The GitHub Actions workflow builds:

- `device-control-test.exe`: the tray application and local control agent
- `Dreem-TikTok-Reader.exe`: the optional TikTok chat reader

The app listens only on `127.0.0.1:17836`, opens the dashboard at
`https://xdreemb52.vercel.app/device-control/`, creates a desktop shortcut, and
keeps the tray icon available near the Windows clock.

This is an isolated test package. It does not replace or modify the Android app
in the repository root.
