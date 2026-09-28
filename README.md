# Nahfunk

Send files to another computer on the same network. No cloud, no account, no size limit —
the two machines talk to each other directly, and a transfer that breaks off picks up where
it stopped instead of starting over.

Available as a single file per system: `Nahfunk.exe` for Windows, `Nahfunk.dmg` for macOS,
a plain binary for Linux. Nothing to install, nothing to configure.

## Getting started

1. Download the file for your system from the [latest release](../../releases/latest) and
   start it. A page opens in your browser.
2. Do the same on the other computer.
3. Both pages now list the other device. Pick it, choose your files, press **Senden**.
4. The first transfer to a device asks for that device's six-digit code — it is shown at the
   top right of its page. It is remembered afterwards.

The receiving side asks before it accepts anything, unless you switch that off in the
settings. Files land in `Downloads/Nahfunk` by default.

## Why transfers do not get lost

Interruptions are the normal case on a home network, so the design assumes them:

- **Resume by byte.** Received data goes into a partial file next to a small journal entry.
  Both sides agree on how many bytes arrived, and the sender continues from that exact byte
  after a dropped connection, a closed laptop or a power cut.
- **The queue survives a crash.** A file you send is copied into a staging folder and
  written into the journal before anything goes over the network. If the app dies, the
  next start finds the job and finishes it.
- **Nothing arrives half-written.** The file is assembled under a temporary name, its
  SHA-256 is checked against what the sender announced, and only then is it moved into
  place. A mismatch keeps the partial file so the rest can be re-sent.
- **Retries back off.** Every failed attempt waits longer than the last, up to a minute,
  for up to 40 attempts. A device that is simply switched off costs nothing in the meantime.
- **No overwriting.** An existing `photo.jpg` becomes `photo (2).jpg`.

## Privacy and safety on the network

- Devices find each other over mDNS (`_nahfunk._tcp`), the same mechanism printers use.
- Each device generates its own TLS certificate on first start and announces that
  certificate's fingerprint. A sender refuses to talk to a device whose certificate does
  not match, so nothing on the network can slip in between.
- Every request carries the receiving side's six-digit code. Without it the answer is 403.
- The user interface listens on loopback only; the network port serves the other device.

## Ports

| Port | Who uses it | Bound to |
|------|-------------|----------|
| 51765 | the other device | all interfaces |
| 51766 | the user interface | 127.0.0.1 |

Change it with `-port 40000`; the interface always sits one port above. If a firewall asks
on first start, allow the app on private networks.

## Command line

The same binary works without the browser page:

```
nahfunk                              # start the service and open the page
nahfunk -ohne-browser                # start it quietly, e.g. on a server
nahfunk geraete                      # list the devices it can see
nahfunk senden urlaub.zip -an Laptop -code 123456
nahfunk -name "Büro-PC" -ziel /srv/eingang
```

## Where its files live

| What | Where |
|------|-------|
| settings, certificate, send queue | `%AppData%\nahfunk` · `~/Library/Application Support/nahfunk` · `~/.config/nahfunk` |
| received files | the folder set in the settings, `Downloads/Nahfunk` by default |
| partial files and journal | `.nahfunk` inside that folder |

## First start warnings

Both systems warn about software they have not seen signed before. This is expected — the
releases are built by GitHub Actions and not signed with a paid certificate.

- **Windows:** SmartScreen → *More info* → *Run anyway*.
- **macOS:** open the `.dmg`, drag `Nahfunk.app` to Applications, then right-click it once
  and choose *Open*. Alternatively `xattr -dr com.apple.quarantine /Applications/Nahfunk.app`.

## Building it yourself

Go 1.22 or newer, no C compiler, no other dependency:

```
go test ./...
go build ./cmd/nahfunk
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -o Nahfunk.exe ./cmd/nahfunk
```

## License

MIT, see [LICENSE](LICENSE).
