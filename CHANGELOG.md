# Changelog

Wszystkie istotne zmiany w projekcie. Format według [Keep a Changelog](https://keepachangelog.com/pl/1.1.0/),
numeracja według [SemVer](https://semver.org/lang/pl/). Numer bieżącej wersji jest w `VERSION.md`;
release powstaje po merge do `master`, który zmienia `VERSION.md`, gdy ten numer jest wyższy od
ostatniego wydanego tagu.

## [Unreleased]

### Dodane

- Karty i komputery pracujące na jednym pliku danych widzą nawzajem swoje zmiany. Karta
  sprawdza plik co 10 sekund, gdy jest widoczna, po powrocie do niej i po zamknięciu okna
  dialogowego, a nowszą wersję wczytuje tylko wtedy, gdy sama nie ma niezapisanych zmian.
  `GET /api/state` zwraca `ETag` (skrót treści pliku) i odpowiada 304 na pasujące
  `If-None-Match`.
- Import kolekcji Postmana: zapytanie bez własnego auth albo z typem `inherit` dostaje auth
  najbliższego folderu, a potem kolekcji, a `noauth` daje brak auth. Zmienne kolekcji
  (`variable`) trafiają do nowego środowiska `<nazwa kolekcji> variables`. OAuth 2.0
  i skrypty pre-request nie są importowane, a powiadomienie po imporcie podaje liczbę
  takich zapytań.
- Import pliku z nowszej wersji importuje to, co ta wersja zna, a o reszcie mówi
  w powiadomieniu, np. `Skipped: tcp (needs a newer simple-rest)`, zamiast
  `Unrecognized format`.

### Zmienione

- `PUT /api/state` wymaga nagłówków `X-Simple-Rest-Version` i `If-Match`. Strona z innej
  wersji programu dostaje 409, a brak `If-Match` daje 428. Gdy plik zmienił się w innej
  karcie albo na innym komputerze, zapis dostaje 412 z `{"error": ...}` i plik zostaje bez
  zmian. W obu przypadkach karta pokazuje baner z przyciskiem Reload i do przeładowania
  niczego nie zapisuje. Karta otwarta jeszcze z wersji 1.1.0 nie ma banera: przy każdej
  próbie zapisu pokazuje komunikat 409 w powiadomieniu „Save failed” i też niczego nie
  nadpisuje.
- Wybór środowiska dotyczy karty i przeżywa jej przeładowanie. Plik pamięta go tylko jako
  środowisko startowe nowej karty.
- Klucze, których ta wersja nie zna (na najwyższym poziomie pliku, w ustawieniach,
  kolekcjach, zapytaniach i środowiskach), przeżywają zapis.
- Wartości z pliku danych i z importu są sprowadzane do właściwych typów: `null`
  w zmiennej daje pusty tekst, liczba daje tekst, a ułamkowy timeout jest obcinany.
- `{{constructor}}` i inne nazwy z prototypu obiektu nie rozwijają się, a `{{__proto__}}`
  działa jak każda zmienna.
- Instalacja aktualizacji najpierw zapisuje dane i nie rusza, jeśli zapis się nie udał.

## [1.1.0] - 2026-09-10

### Zmienione

- Wydanie to jeden plik na system: Windows x64, Linux x64 i Raspberry Pi 64-bit, plus plik
  z sumami SHA-256. Wcześniej było osiem binarek.

### Usunięte

- Build dla Androida. `android/arm64` linkuje się do `/system/bin/linker64`, a bez CGO Go
  używa własnego resolvera DNS, który na Androidzie nie działa (komentarz w `net/conf.go`,
  golang/go#10714), więc plik nie nadawał się do klienta REST. Z telefonu działa wejście
  przeglądarką na instancję uruchomioną w sieci (`-listen 0.0.0.0:8787`).
- Buildy macOS (arm64 i amd64), Windows na ARM oraz 32-bitowy Raspberry Pi. Budują się
  jedną komendą z README; taka kopia mówi w ustawieniach, że nie ma dla niej pliku
  wydania, i nie próbuje się aktualizować.

## [1.0.0] - 2026-09-10

### Dodane

- Klient REST w jednym pliku binarnym: serwer na localhost z wbudowanym UI, zapytania
  wykonuje program, więc nie obowiązują ograniczenia CORS przeglądarki.
- Metody GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS; params synchronizowane z URL;
  headers; body raw (JSON, text, XML, HTML, JS) lub x-www-form-urlencoded; auth Basic,
  Bearer i API key; opcje timeout, redirecty, weryfikacja TLS, cookie jar.
- Odpowiedź ze statusem, czasem, rozmiarem i protokołem, kolorowany JSON, widok raw,
  podgląd HTML i obrazków, kopiowanie i zapis body.
- Kolekcje, historia, środowiska ze zmiennymi `{{nazwa}}`, import/export JSON, import
  Postman Collection v2.1 i Postman Environment, kopiowanie jako cURL.
- Automatyczna aktualizacja z aplikacji: sprawdzanie raz dziennie (domyślnie wyłączone),
  pobranie pliku dla platformy z weryfikacją rozmiaru i SHA-256, podmiana binarki
  i restart.
- Proces wydawania: testy Go i JS w CI na każdym PR, release po merge do `master` gdy
  `VERSION.md` rośnie, kopia paczek do repozytorium Mysttic/Mysttic.
