# simple-rest

Minimalny klient REST w stylu Postmana: jeden plik binarny, bez instalacji, bez logowania,
bez zależności. Interfejs działa w przeglądarce, ale zapytania HTTP wykonuje sam program,
więc nie obowiązują ograniczenia CORS przeglądarki.

Pobieranie: https://mysttic.github.io/Mysttic/simple-rest/

## Uruchomienie

Program otwiera przeglądarkę pod adresem `http://127.0.0.1:8787/`. Pliki nazywają się
`simple-rest-<wersja>-<platforma>`.

| System | Plik | Jak |
|---|---|---|
| Windows | `simple-rest-<wersja>-windows-amd64.exe` | dwuklik lub z konsoli |
| Windows na ARM | `...-windows-arm64.exe` | jak wyżej |
| Linux x64 | `...-linux-amd64` | `chmod +x plik && ./plik` |
| Raspberry Pi (64-bit OS) | `...-linux-arm64` | jak wyżej |
| Raspberry Pi (32-bit OS, Zero, Pi 1/2) | `...-linux-armv6` | jak wyżej |
| macOS | `...-macos-arm64` / `...-macos-amd64` | jak wyżej |
| Android (Termux) | `...-android-arm64` | skopiować do `~/` w Termuxie, `chmod +x`, uruchomić, otworzyć `http://127.0.0.1:8787/` w Chrome |
| Android bez Termuxa | dowolna binarka na PC | uruchomić na PC z `-listen 0.0.0.0:8787`, na telefonie otworzyć `http://<ip-pc>:8787/` |

Flagi:

```
-listen 127.0.0.1:8787   adres nasłuchu (0.0.0.0:8787 udostępnia UI w sieci lokalnej)
-data <plik>             plik z danymi (domyślnie simple-rest-data.json obok binarki)
-no-browser              nie otwieraj przeglądarki automatycznie
-version                 wypisz wersję i zakończ
-update-url <adres>      skąd updater pobiera listę wydań (tylko do testów)
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

## Aktualizacje z aplikacji

Ustawienia (ikona koła zębatego), sekcja Updates:

- **Check for updates** pyta publiczne repozytorium Mysttic/Mysttic o najnowsze wydanie
  z prefiksem `simple-rest-v`, porównuje numer z uruchomionym i pokazuje jeden z trzech
  stanów: jest nowsza wersja (karta z numerem, notatką i przyciskiem), to jest najnowsza
  wersja, albo nie udało się zapytać (problem z połączeniem, osobny komunikat).
- **Automatyczne sprawdzanie** jest domyślnie wyłączone. Po włączeniu program pyta raz
  na dobę (timer co godzinę, pytanie tylko gdy ostatnie było ponad 24 h temu; znacznik
  czasu zapisuje się także po nieudanej próbie). Przycisk działa niezależnie od tego
  ustawienia.
- **Install and restart** pobiera plik dla tej platformy do `.simple-rest-update/` obok
  binarki (najpierw jako `.part`), sprawdza rozmiar zadeklarowany w wydaniu i SHA-256
  z pliku `...-checksums.txt`, podmienia własny plik wykonywalny i uruchamia się ponownie
  na tym samym porcie. Strona w przeglądarce czeka na nową wersję i sama się przeładowuje.
  Na Windows stary plik zostaje chwilowo jako `.exe.old` i jest usuwany przy starcie.
- Kopia, która nie może się zaktualizować (katalog bez prawa zapisu, uruchomienie przez
  `go run`, brak pliku dla platformy), mówi to wprost i linkuje do strony pobierania.
- Stan updatera trzymany jest w `simple-rest-update.json` obok pliku danych.

Pełne przejście ścieżki na prawdziwej binarce (kopia 0.9.0, fałszywy hub, podmiana,
restart) uruchamia `sh scripts/e2e-update.sh`; to samo robi CI.

## Dane

Wszystko (kolekcje, historia, środowiska, ustawienia) trafia do jednego pliku JSON obok
binarki: `simple-rest-data.json`. Wystarczy przenieść katalog, żeby przenieść dane.
Aktualizacja nie dotyka tego pliku.

## Budowanie

Potrzebny tylko Go (https://go.dev/dl/), bez CGO i bez zewnętrznych modułów. Numer
wersji jest wkompilowany z `VERSION.md`.

```
go build -ldflags "-s -w" -o simple-rest.exe .
```

Wszystkie platformy naraz do `dist/` (osiem binarek i plik z sumami SHA-256):

```
sh build.sh        # Linux/macOS/Git Bash
build.cmd          # Windows cmd
```

Kopia zgłaszająca inny numer, do testów updatera:
`go build -ldflags "-X main.versionOverride=0.9.0" -o simple-rest.exe .`

## Rozwój i testy

Gałęzie: `develop` (praca) i `master` (wydania). Zmiany wchodzą przez PR; przed merge
CI musi przejść.

```
go test ./... -count=1                 # serwer, proxy, guard, updater (z fałszywym hubem)
node --test "tests/**/*.test.mjs"      # czyste funkcje UI: params/URL, zmienne, auth, import Postman, cURL
sh scripts/e2e-update.sh               # ścieżka aktualizacji end to end na prawdziwej binarce
```

Workflow `CI` (`.github/workflows/ci.yml`) uruchamia to wszystko na każdym PR do `develop`
i `master` oraz przy push do `develop`, do tego `gofmt`, `go vet` i build wszystkich
platform. PR do `master` dostaje dodatkowo job „Release preview” z informacją, czy merge
opublikuje wydanie.

## Wydania

- `VERSION.md` trzyma numer wersji (jedna linia, `major.minor.patch`). Program wkompilowuje
  go przy budowaniu, więc binarka i tag zawsze się zgadzają.
- `CHANGELOG.md` opisuje zmiany w każdej wersji (format Keep a Changelog). Wydanie wymaga
  sekcji `## [x.y.z]` dla numeru z `VERSION.md`; jej treść staje się opisem release.
- Workflow `Release` (`.github/workflows/release.yml`) startuje po każdym push do `master`
  (czyli po merge PR z `develop`) i publikuje tylko wtedy, gdy `VERSION.md` jest wyższy od
  najnowszego tagu `v*`. Wtedy: testy, build, tag `v<wersja>` i release w tym repozytorium
  z plikami z `dist/`, a potem job `site` kopiuje te same pliki do Mysttic/Mysttic pod
  tagiem `simple-rest-v<wersja>` i usuwa tam starsze wydania projektu. Bez podniesienia
  `VERSION.md` merge nie publikuje nic. Obniżenie numeru zatrzymuje workflow z błędem.

Procedura wydania:

1. Na `develop`: podnieść `VERSION.md`, dopisać sekcję w `CHANGELOG.md`.
2. PR `develop` → `master`, poczekać na zielone CI i podgląd „Release preview”.
3. Merge. Release i kopia na hub dzieją się same.

Wymagany sekret: `HUB_TOKEN` w ustawieniach repozytorium (Settings, Secrets and variables,
Actions): fine-grained token dla właściciela `Mysttic`, dostęp tylko do `Mysttic/Mysttic`,
uprawnienie Contents: Read and write. Bez niego release w tym repozytorium powstanie, a job
`site` zakończy się czytelnym błędem.

Strona projektu na hubie leży gotowa w `site/simple-rest/`; jak ją podpiąć opisuje
`site/README.md`.

## Tryb bez binarki

`ui.html` otwarty bezpośrednio w przeglądarce (także na Androidzie) też działa, ale wtedy
zapytania wysyła przeglądarka: serwer docelowy musi zezwalać na CORS, opcje TLS i cookies
są ignorowane, dane trzymane są w localStorage tej przeglądarki, a sekcja aktualizacji
jest ukryta.

## Bezpieczeństwo

Domyślnie program nasłuchuje tylko na 127.0.0.1 i odrzuca żądania z obcym nagłówkiem
Host lub Origin (ochrona przed DNS rebinding i CSRF ze stron otwartych w tej samej
przeglądarce). `-listen 0.0.0.0:8787` wystawia proxy HTTP bez uwierzytelniania na całą
sieć lokalną, więc używaj tego tylko w zaufanej sieci. Jedyne połączenie wychodzące, które
program nawiązuje sam z siebie, to sprawdzenie wydań, i to dopiero po włączeniu.
