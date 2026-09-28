# Security

## Reporting something

Open a [private advisory](../../security/advisories/new) if you find a weakness. Please do
not open a normal issue for it. There is no bounty; this is a hobby project.

## What the design assumes

Quicksend is meant for a network you already trust as far as printers and TVs are
concerned: a home or office LAN. It is not meant to be reachable from the internet, and
port 51765 should not be forwarded on a router.

Within that setting it defends against the obvious moves:

| Move | What stops it |
|------|---------------|
| Reading a transfer off the wire | Everything between the devices runs over TLS 1.2+. |
| Pretending to be the other device | The certificate fingerprint is announced over mDNS and pinned by the sender; a different certificate ends the attempt. |
| Talking to a device that never agreed | Every request needs that device's six digit code, compared in constant time; without it the answer is 403. |
| Guessing the code | After five wrong codes an address waits 30 seconds, and the wait doubles per further miss up to 15 minutes. |
| A web page in the browser driving the interface | The interface listens on 127.0.0.1 only, refuses any other `Host` (DNS rebinding), rejects cross-site writes via `Sec-Fetch-Site` and `Origin`, and sends a strict content security policy. |
| A file writing outside the target folder | Only the base name of an incoming file is used, and an existing file is never overwritten. |
| A tampered download | Every release carries SHA-256 sums and a build attestation from GitHub Actions. |

## What it does not do

- **No end-to-end identity.** The fingerprint tells you the device you first saw is the
  device you are talking to now. It does not tell you who owns it. The code is the only
  thing standing between "on the same network" and "may send you files".
- **No signed binaries.** Releases are not signed with a paid Apple or Windows
  certificate, so both systems warn on first start. The macOS bundle carries an ad-hoc
  signature, which seals it but is not a developer identity.
- **No protection against someone at the keyboard.** Settings, the code and the
  certificate sit in the user's config folder with user-only permissions, unencrypted.

## Checking a download

```
sha256sum -c pruefsummen-windows-linux.txt
gh attestation verify Quicksend.exe -R Dschonas04/quicksend
```

The second command asks GitHub whether that exact file came out of this repository's
release workflow.
