# simple-rest

Minimalny klient REST w stylu Postmana: jeden plik binarny, bez instalacji, bez logowania,
bez zależności. Interfejs działa w przeglądarce, ale zapytania HTTP wykonuje sam program,
więc nie obowiązują ograniczenia CORS przeglądarki.

## Uruchomienie

Gotowe binarki po zbudowaniu leżą w `dist/` (patrz "Budowanie"). Program otwiera
przeglądarkę pod adresem `http://127.0.0.1:8787/`.

| System | Plik | Jak |
|---|---|---|
| Windows | `simple-rest-windows-amd64.exe` | dwuklik lub z konsoli |
| Linux x64 | `simple-rest-linux-amd64` | `chmod +x simple-rest-linux-amd64 && ./simple-rest-linux-amd64` |
| Raspberry Pi (64-bit OS) | `simple-rest-linux-arm64` | jak wyżej |
| Raspberry Pi (32-bit OS, Zero, Pi 1/2) | `simple-rest-linux-armv6` | jak wyżej |
| macOS | `simple-rest-macos-arm64` / `-amd64` | jak wyżej |
| Android (Termux) | `simple-rest-android-arm64` | skopiować do `~/` w Termuxie, `chmod +x`, uruchomić, otworzyć `http://127.0.0.1:8787/` w Chrome |
| Android bez Termuxa | dowolna binarka na PC | uruchomić na PC z `-listen 0.0.0.0:8787`, na telefonie otworzyć `http://<ip-pc>:8787/` |

Flagi:

```
-listen 127.0.0.1:8787   adres nasłuchu (0.0.0.0:8787 udostępnia UI w sieci lokalnej)
-data <plik>             plik z danymi (domyślnie simple-rest-data.json obok binarki)
-no-browser              nie otwieraj przeglądarki automatycznie
```

Jeśli port 8787 jest zajęty, program wybiera losowy wolny port i wypisuje adres w konsoli.

## Funkcje

- Metody GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS.
- Params (synchronizowane z URL w obie strony), Headers, Body (raw JSON/text/XML/HTML/JS
  albo x-www-form-urlencoded), Auth (Basic, Bearer, API key w nagłówku lub query).
- Opcje per zapytanie: timeout, podążanie za przekierowaniami, weryfikacja TLS, cookie jar.
- Odpowiedź: status, czas, rozmiar, protokół, nagłówki, podgląd JSON z kolorowaniem,
  widok raw, podgląd HTML/obrazków, kopiowanie i zapis body do pliku.
- Kolekcje z zapisanymi zapytaniami, historia (200 ostatnich), środowiska ze zmiennymi
  `{{nazwa}}` podstawianymi w URL, params, nagłówkach, body i auth.
- Import/Export do JSON. Import rozumie też Postman Collection v2.1 i Postman Environment.
- Kopiowanie zapytania jako polecenie `curl`.
- Skróty: Enter w polu URL lub Ctrl+Enter wysyła, Ctrl+S zapisuje.

## Dane

Wszystko (kolekcje, historia, środowiska, ustawienia) trafia do jednego pliku JSON obok
binarki: `simple-rest-data.json`. Wystarczy przenieść katalog, żeby przenieść dane.

## Budowanie

Potrzebny tylko Go (https://go.dev/dl/), bez CGO i bez zewnętrznych modułów.

```
go build -ldflags "-s -w" -o simple-rest.exe .
```

Wszystkie platformy naraz do `dist/`:

```
sh build.sh        # Linux/macOS/Git Bash
build.cmd          # Windows cmd
```

## Tryb bez binarki

`ui.html` otwarty bezpośrednio w przeglądarce (także na Androidzie) też działa, ale wtedy
zapytania wysyła przeglądarka: serwer docelowy musi zezwalać na CORS, opcje TLS i cookies
są ignorowane, a dane trzymane są w localStorage tej przeglądarki.

## Bezpieczeństwo

Domyślnie program nasłuchuje tylko na 127.0.0.1 i odrzuca żądania z obcym nagłówkiem
Host lub Origin (ochrona przed DNS rebinding i CSRF ze stron otwartych w tej samej
przeglądarce). `-listen 0.0.0.0:8787` wystawia proxy HTTP bez uwierzytelniania na całą
sieć lokalną, więc używaj tego tylko w zaufanej sieci.
