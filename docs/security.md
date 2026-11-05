# Security

Part of the [simple-rest documentation](../README.md#documentation).

By default the program listens on 127.0.0.1 only and refuses requests with a foreign Host or
Origin header (protection against DNS rebinding and against CSRF from pages open in the same
browser).

When it listens beyond 127.0.0.1 (`-listen 0.0.0.0:8787`, for instance), the Host header has to
be an IP address (IPv6 too, also with a zone), a name without a dot, a name with an ending that
cannot be registered in public DNS (`.local`, `.lan`, `.home`, `.home.arpa`, `.internal`,
`.localdomain`), the computer name the system reports or its first label, or a name given with
`-allow-host`. Letter case and a trailing dot do not matter. This way a foreign page cannot
point its own name at this computer through DNS rebinding to read the program's answers.
Any other name gets 403 with a hint, and the first time a name is refused the console gives the
ready flag, such as `-allow-host pc123.example.com`. A name with characters other than letters,
digits, hyphen, underscore and dot gets no ready flag. `-allow-host "*"` turns the check off, as
before version 1.2.0, and the program warns about it at start. The star in double quotes works
in cmd, PowerShell and sh; in cmd single quotes (`'*'`) reach the program together with the star,
and the check stays on. When the program listens on 127.0.0.1, the flag changes nothing.

The program's page cannot be embedded in a frame on another page (`X-Frame-Options: DENY` and
`Content-Security-Policy: frame-ancestors 'none'`), so a foreign page cannot slip you a click on
Send, Enabled, Run now or Install and restart.

The Linux builds do not ask for `.local` names over mDNS, only over the network's ordinary DNS
(see [From another device](usage.md#from-another-device)). Usually that ends with a
`no such host` error and a hint, but a DNS server that knows such a name may point to another
computer than the one that announces it on the network, and that is where a schedule's login
with its password would go. On Linux and on the Raspberry Pi give the token server and other
targets by IP address or by a name from the network's DNS.

The program has no password. `-listen 0.0.0.0:8787` exposes an HTTP proxy without
authentication to the whole local network: anyone who reaches the program sees and changes
everything it holds, that is the collections, the history and the environments with the values
of the variables (passwords and tokens too), the values set by schedules (`GET /api/schedules`)
and the TCP console log (patient data from an analyser, for instance), and sends requests from
this machine, also turning schedules on and off. The TCP console sends any bytes to any host and
port reachable from the machine with the binary, so anyone on the network can talk to devices or
scan the network through the program. Use this only on a network you trust. The `/api/tcp*`
routes and the `/api/events` event stream pass the same Host and Origin checks as the rest. On
its own the program connects in two cases only, and only after you turn them on: to check for
releases and to send a schedule's request. A TCP connection is opened only by Connect in the UI.
A schedule turned on from another computer keeps running after that tab is closed; the
Schedules window shows since when it has been on and from which address. Likewise a TCP
connection stays open after its tab is closed, and the console shows from which address it was
opened.
