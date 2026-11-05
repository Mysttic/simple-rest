# Updates and data

Part of the [simple-rest documentation](../README.md#documentation).

## Updates from the app

Settings (the gear icon), section Updates:

- **Check for updates** asks the public repository Mysttic/Mysttic for the newest release with
  the `simple-rest-v` prefix, compares its number with the running one and shows one of three
  states: a newer version is out (a card with the number, a note and a button), this is the
  newest version, or the check failed (a connection problem, with a message of its own).
- **Automatic checks** are off by default. Once turned on, the program asks once a day (a timer
  every hour, asking only when the last check was more than 24 h ago; the time is saved after a
  failed attempt too). The button works whatever this setting is.
- **Install and restart** downloads the file for this platform into `.simple-rest-update/` next
  to the binary (first as `.part`), checks the size the release declares and the SHA-256 from
  the `...-checksums.txt` file, swaps its own executable and starts again on the same port, with
  the same arguments. The page in the browser waits for the new version and reloads by itself.
  If the new version refuses the name the page was opened by (the Host rule, see
  [Security](security.md)), a page from version 1.2.0 or newer says so plainly. A page from a
  version older than 1.2.0 cannot: whoever opens the UI, while the program listens beyond
  127.0.0.1, by a name outside the rule, such as `pc123.example.com`, sees after the update to
  1.2.0, after about 40 seconds, a message that the program did not come back, although the new
  version runs on the same port. Then open the UI by its IP address, or stop the program and
  start it with `-allow-host pc123.example.com`. Do not start a second copy next to the running
  one: the port is taken, so it would come up on a random port, on the same data file.
  On Windows the old file stays for a moment as `.exe.old` and is removed at the next start.
  A restart turns all schedules off, clears their values and closes the TCP connections, so with
  schedules on or connections open the button asks first. The other open tabs show a
  notification after the restart that the schedules have to be turned on again.
- A copy that cannot update itself (a directory it cannot write to, a start through `go run`, no
  release file for this system) says so plainly and links to the download page.
- The updater keeps its state in `simple-rest-update.json` next to the data file.

The whole path on a real binary (a 0.9.0 copy, a fake hub, the swap, the restart) is walked by
`sh scripts/e2e-update.sh`; CI does the same.

## Data

Everything (collections, history, environments, settings, the definitions of schedules and TCP
connections) goes into one JSON file next to the binary: `simple-rest-data.json`. Move the
directory and the data moves with it. An update does not touch this file.

Several tabs can work with the same file at once, also from other computers with
`-listen 0.0.0.0:8787`. A visible tab gets a signal from the program when the file has changed
(the event stream, see [TCP console](tcp-console.md)), and without the stream it checks the file
every 10 seconds; it also checks it when you come back to the tab and after a dialog closes. It
loads the newer version only when it has no unsaved changes of its own. A request open in the
editor without unsaved changes is refreshed too, and one with unsaved changes stays and shows the
difference against the new version. The TCP console also shows the connection definition from
the file.

Every save goes with an `If-Match` header. If another tab or another computer changed the file
in the meantime, the program refuses the save and the file stays as it was. The tab then shows a
banner with a Reload button and saves nothing until it is reloaded, so the last change has to be
made again after the reload. A page opened from another version of the program (a tab from
before an update, for instance) overwrites nothing either. A page from version 1.2.0 or newer
shows the same banner then, and a tab from version 1.1.0 or older shows a "Save failed"
notification at every attempt to save, asking to reload the page.

The environment chosen on the top bar applies only to the current tab and survives its reload.
The file remembers the last choice as the starting environment of a new tab.

The schedule definitions are in the file under the `schedules` key. Whether a schedule is on,
when it runs next, why it stopped and which values it set, the program keeps in memory only, and
it never writes the data file by itself.

The TCP connection definitions with their saved messages are in the file under the `tcp` key.
The state of the connections and their log are only in the program's memory, and the text of
the message editor is a draft in the browser (localStorage), separate for each connection.
Note: after going back to version 1.1.0, the first save from a tab removes the `schedules` and
`tcp` keys from the file.

Fields this version does not know, written by a newer version for instance, survive a save
unchanged.
