# Using simple-rest

Part of the [simple-rest documentation](../README.md#documentation).

## Starting

The program opens your browser at `http://127.0.0.1:8787/`. If port 8787 is taken, it picks a
free port at random and prints the address on the console.

```
-listen 127.0.0.1:8787   listen address (0.0.0.0:8787 makes the UI reachable on the local network)
-allow-host <names>      more host names the UI may be opened by when listening beyond
                         127.0.0.1, comma-separated; "*" accepts any name
-data <file>             data file (default: simple-rest-data.json next to the executable)
-no-browser              do not open the browser
-version                 print the version and exit
-update-url <url>        where the updater asks for the list of releases (for tests only)
```

## From another device

To use the UI from another device on the network (a phone, a tablet, a second machine), start
the program with `-listen 0.0.0.0:8787` and open `http://<machine-ip>:8787/`. The UI is an
ordinary web page, so it works on any system with a browser, also where there is no binary.
Instead of the IP address you can use a local name: a name without a dot
(`http://raspberrypi:8787/`), a name ending in `.local`, `.lan`, `.home`, `.home.arpa`,
`.internal` or `.localdomain`, or the computer name the system reports. Any other name, such as
`pc123.example.com` from a company DNS, has to be added with `-allow-host pc123.example.com`
(see [Security](security.md)).

The program sends the requests, runs the schedules and opens the TCP connections, so all of it
happens on the machine with the binary, a Raspberry Pi for instance. The target server and the
TCP device have to be reachable from that machine, not from the computer with the browser. The
TCP console works the same from another computer on the network: connections are shared by all
tabs and computers (see [TCP console](tcp-console.md)).

The Linux builds are made without CGO and do not resolve `.local` names (mDNS): on Linux and on
the Raspberry Pi give an IP address or a name from the network's DNS. On Windows `.local` names
work as they do in the system.

## Requests, import and shortcuts

- Methods GET, POST, PUT, PATCH, DELETE, HEAD and OPTIONS.
- Params (kept in sync with the URL both ways), Headers, Body (raw JSON, text, XML, HTML or JS,
  or x-www-form-urlencoded) and Auth (Basic, Bearer, an API key in a header or in the query).
- Options per request: timeout, following redirects, TLS verification, cookie jar.
- The response: status, time, size, protocol, headers, a JSON preview with colours, a raw view,
  an HTML and image preview, copying and saving the body to a file.
- Collections of saved requests, a history of the last 200, and environments whose `{{name}}`
  variables are substituted in the URL, params, headers, body and auth.
- Schedules: the program sends a saved request, a login for instance, every so often and sets a
  variable, such as `{{token}}`, with a value from the answer (see [Schedules](schedules.md)).
- The TCP console: the program connects to a device or system on the network, sends messages
  with `{{variables}}` (text with escapes, or hex), splits what comes in into messages by the
  framing (None, LF, CR LF, MLLP, STX/ETX) and times the answers (see
  [TCP console](tcp-console.md)).
- Import and export to JSON, with the definitions of the schedules (without the values they set)
  and of the TCP connections with their saved messages (without the log).
  Import also reads Postman Collection v2.1 and Postman Environment files. A Postman request
  without auth of its own (or with `inherit`) gets the auth of its nearest folder, then of the
  collection; `noauth` means no auth. Collection variables go into a new environment named
  `<collection name> variables`. OAuth 2.0 and pre-request scripts are not imported, and the
  notification after the import gives the number of such requests. Sections of a file from a
  newer simple-rest that this version does not know are skipped and listed in the notification.
- Copy a request as a `curl` command.
- Shortcuts: Enter in the URL field or Ctrl+Enter sends, Ctrl+S saves. In the TCP console
  Ctrl+Enter sends the message from the editor, and Ctrl+S saves it under a name with the
  connection (Save as…).

## Without the program

`ui.html` opened straight in a browser works too, but then the browser sends the requests: the
target server has to allow CORS, the TLS and cookie options are ignored, the data is kept in
that browser's localStorage and the updates section is hidden. Schedules run only in the
program: the Schedules window shows the definitions, imported from a file for instance, but
read-only, and Send does not ask for fresh values. The TCP console needs the program, because a
browser does not open raw TCP sockets: without it the TCP tab shows the connection definitions
read-only, under a banner. Likewise a page opened from another version of the program (a tab
from before an update, for instance) shows the schedules and the TCP console under a banner and
without actions (Connect, Send and Disconnect included) until you reload it; HTTP Send keeps
working.

## Other systems

macOS, Windows on ARM and a 32-bit Raspberry Pi have no ready file, but they build with one
command (see [Building](../CONTRIBUTING.md#building)). Such a copy says in Settings that there is
no release file for it and does not try to update itself.
