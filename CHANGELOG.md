# Changelog

Wszystkie istotne zmiany w projekcie. Format według [Keep a Changelog](https://keepachangelog.com/pl/1.1.0/),
numeracja według [SemVer](https://semver.org/lang/pl/). Numer bieżącej wersji jest w `VERSION.md`;
release powstaje po merge do `master`, który zmienia `VERSION.md`, gdy ten numer jest wyższy od
ostatniego wydanego tagu.

## [Unreleased]

## [1.2.0] - 2026-10-01

### Dodane

- Harmonogramy (kanał cykliczny) działają w programie, także bez otwartej karty: harmonogram
  co zadany czas wysyła zapisane zapytanie i ustawia zmienną, np. `{{token}}`, wartością
  z odpowiedzi (ścieżka JSON, nagłówek albo regex). Termin wynika z `everySec` albo z czasu
  ważności z odpowiedzi (np. `expires_in`) minus zapas. Wartość trafia do nakładki
  w pamięci programu, która w swoim środowisku wygrywa z wierszami tabeli. Definicje leżą
  w pliku danych pod kluczem `schedules`, ale stan włączenia, terminy, blokady i nakładka
  są tylko w pamięci: po każdym starcie programu, także po aktualizacji, wszystkie
  harmonogramy są wyłączone, a program nigdy nie zapisuje pliku danych. Odrzucone
  logowanie (401, 403), przekierowanie i inne odpowiedzi 4xx zatrzymują harmonogram po
  pierwszej próbie, a 5xx, 408 i 429 po pięciu porażkach z rzędu. API:
  `GET /api/schedules`, `POST /api/schedules/{id}/enable`, `.../disable` i `.../run`,
  `POST /api/schedules/disable-all`, `POST /api/vars/ensure` oraz
  `DELETE /api/vars/{envId}/{name}`.
  Uwaga: po cofnięciu do 1.1.0 pierwszy zapis z karty usuwa z pliku klucz `schedules`
  (definicje harmonogramów).
- Okno Schedules z przyciskiem na górnym pasku. Przycisk pokazuje liczbę włączonych
  harmonogramów, czerwoną ramkę, gdy któryś stoi albo nie może ruszyć, i `?`, gdy program
  nie odpowiada. Okno ma listę ze stanem (`ok 2m`, `retry 0:30`, `stopped`, `no request`,
  `missing {{password}}` itd.), edycję definicji (zapytanie, środowisko, Every, ścieżka
  czasu ważności i zapas, reguły JSON, nagłówek i regex z Opt), Enabled z godziną i adresem,
  z którego harmonogram włączono, Run now, Delete i Turn all off oraz ostatni i następny
  przebieg, zamaskowane wartości z Copy, pola odpowiedzi, powód zatrzymania z początkiem
  odpowiedzi (z odpowiedzi 2xx tylko klucze i liczby JSON, teksty zamaskowane) i podpowiedzi
  (problemy definicji, inne środowisko karty z przyciskiem Select it, ta sama zmienna z dwóch
  harmonogramów, brakująca zmienna, składnia RE2, niezapisane zmiany zapytania). Enabled
  i Run now nie ruszają, gdy zmian karty nie udało się zapisać. Nowy harmonogram
  (+ Schedule albo przycisk ⟳ przy zapytaniu na pasku bocznym) jest wyłączony, a bez
  środowisk powstaje środowisko `Default`. Znacznik ⟳ na pasku bocznym pokazuje zapytania
  wysyłane przez włączony harmonogram.
- Okno Environments pokazuje pod tabelą wartości ustawione przez harmonogramy: kto i kiedy
  je ustawił, do kiedy są ważne, czy zastępują wiersz tabeli, z przyciskami Copy i Clear.
- Send i cURL zapisują najpierw zmiany karty i proszą program o świeże wartości zmiennych
  zapytania, które ustawiają harmonogramy środowiska karty (`POST /api/vars/ensure`,
  najwyżej 25 sekund). Gdy to trwa dłużej niż 300 ms, status pokazuje `Refreshing token…`.
  Brak odpowiedzi nie blokuje wysyłki, a powiadomienie mówi, gdy wartość może być stara
  albo jej brak, bo harmonogram stoi, się nie udał, jest wyłączony albo wartość wygasła.
- Karta pyta program o harmonogramy co 2 sekundy przy otwartym oknie Schedules albo
  Environments, co 10 sekund, gdy jest widoczna, od razu po akcji i po powrocie do karty,
  a w tle wcale. Odpowiedź niesie też znacznik pliku danych, więc karta pobiera plik tylko
  wtedy, gdy się zmienił. Po restarcie programu karta mówi, że harmonogramy są wyłączone,
  o ile któryś był włączony albo miał wartości.
- Bez programu, a także ze strony z innej wersji programu, okno Schedules pokazuje
  definicje tylko do odczytu, z banerem, a akcje harmonogramów i odświeżanie wartości przed
  Send są wyłączone. Send działa dalej.
- Błąd `no such host` dla nazwy `.local` przy ręcznym Send w programie na Linuksie dostaje
  tę samą podpowiedź co harmonogram: wersje linuksowe nie rozwiązują nazw mDNS.
- Import kolekcji Postmana: zapytanie bez własnego auth albo z typem `inherit` dostaje auth
  najbliższego folderu, a potem kolekcji, a `noauth` daje brak auth. Zmienne kolekcji
  (`variable`) trafiają do nowego środowiska `<nazwa kolekcji> variables`. OAuth 2.0
  i skrypty pre-request nie są importowane, a powiadomienie po imporcie podaje liczbę
  takich zapytań. Auth zapisane jako obiekt (Postman v2.0) też jest czytane.
- Eksport zawiera definicje harmonogramów, bez ustawionych wartości. Import przypina je do
  nowych identyfikatorów zapytań i środowisk z tego samego pliku, a harmonogram bez swojego
  zapytania albo środowiska pomija i podaje ich liczbę w powiadomieniu.
- Flaga `-allow-host a,b` dopisuje nazwy hosta, pod którymi wolno otworzyć UI przy nasłuchu
  poza 127.0.0.1. `-allow-host "*"` przyjmuje każdą nazwę, czyli wyłącza ochronę przed DNS
  rebinding, a program ostrzega o tym przy starcie. Gwiazdka w cudzysłowie podwójnym
  działa w cmd, PowerShell i sh.
- Import pliku z nowszej wersji importuje to, co ta wersja zna, a o reszcie mówi
  w powiadomieniu, np. `Skipped: tcp (needs a newer simple-rest)`, zamiast
  `Unrecognized format`.
- Jedno powiadomienie po imporcie podsumowuje wszystkie wybrane pliki, także te, których
  nie udało się zaimportować, i znika tym później, im jest dłuższe. Plik, którego nie da
  się odczytać, nie przerywa importu pozostałych.
- Karty i komputery pracujące na jednym pliku danych widzą nawzajem swoje zmiany. Karta
  sprawdza plik co 10 sekund, gdy jest widoczna, po powrocie do niej i po zamknięciu okna
  dialogowego, a nowszą wersję wczytuje tylko wtedy, gdy sama nie ma niezapisanych zmian.
  `GET /api/state` zwraca `ETag` (skrót treści pliku) i odpowiada 304 na pasujące
  `If-None-Match`.

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
- Przy nasłuchu poza 127.0.0.1 (np. `-listen 0.0.0.0:8787`) nagłówek Host musi być adresem
  IP, nazwą bez kropki, nazwą z końcówką `.local`, `.lan`, `.home`, `.home.arpa`,
  `.internal` albo `.localdomain`, nazwą komputera albo nazwą z `-allow-host`. Inna nazwa
  dostaje 403 z podpowiedzią, a konsola przy pierwszym odrzuceniu danej nazwy podaje gotową
  flagę. Linia startowa wymienia dozwolone sposoby wejścia. Przy nasłuchu na 127.0.0.1 nic
  się nie zmienia.
- Gdy program po aktualizacji odrzuca nazwę, pod którą otwarto stronę, strona z wersji 1.2.0
  i nowszej mówi to wprost zamiast po kilkudziesięciu sekundach zgłosić, że program nie
  wrócił. Strona z wersji starszej niż 1.2.0 tego nie umie: kto przy nasłuchu poza
  127.0.0.1 otwiera UI pod nazwą spoza reguły Host, np. `pc123.firma.pl`, po Install and
  restart do 1.2.0 zobaczy po około 40 sekundach komunikat, że program nie wrócił, choć
  nowa wersja działa. Wtedy wystarczy otworzyć UI po adresie IP albo zatrzymać program
  i uruchomić go z `-allow-host pc123.firma.pl`. Druga kopia uruchomiona obok działającej
  wstałaby na losowym porcie, bo port jest zajęty.
- Strony programu nie da się osadzić w ramce na innej stronie (`X-Frame-Options: DENY`
  i `Content-Security-Policy: frame-ancestors 'none'`).
- `{{constructor}}` i inne nazwy z prototypu obiektu nie rozwijają się, a `{{__proto__}}`
  działa jak każda zmienna.
- Wartości z pliku danych i z importu są sprowadzane do właściwych typów: `null`
  w zmiennej daje pusty tekst, liczba daje tekst, a ułamkowy timeout jest obcinany.
- Klucze, których ta wersja nie zna (na najwyższym poziomie pliku, w ustawieniach,
  kolekcjach, zapytaniach i środowiskach), przeżywają zapis.
- SIGTERM (np. `systemctl stop`) i zamknięcie okna konsoli na Windows kończą program
  porządnie, tak jak Ctrl+C: zapytania harmonogramów w locie są przerywane, a konsola
  dostaje zaległe linie. To samo dzieje się przed restartem po aktualizacji.
- Instalacja aktualizacji najpierw zapisuje dane i nie rusza, jeśli zapis się nie udał.
  Przy włączonych harmonogramach najpierw pyta, bo restart je wyłącza.
- Potwierdzenia usunięcia zapytania, kolekcji i środowiska wymieniają harmonogramy, które
  z nich korzystają.
- `GET /api/info` zwraca pole `boot`, nowe przy każdym starcie programu.

### Naprawione

- Timeout zapytania tak duży, że w nanosekundach przekracza zakres liczby 64-bitowej,
  mógł przerwać zapytanie od razu. Teraz jest przycinany do około 292 lat.
- Po nieudanym wysłaniu zmiana widoku odpowiedzi (Pretty, Raw, Preview) kasowała
  komunikat błędu albo kończyła się błędem w konsoli. Komunikat zostaje.

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
