# Issue #7: plan zmian (harmonogramy i konsola TCP), wersja 2

Ścieżki w planie są względne wobec `C:\Workspaces\simple-rest\`. Oznaczenia:
- `Lnnn` to linia w `ui.html`;
- `main.go:nnn` to linia w Go;
- `README:nnn` to linia README na `origin/master` (`ccc0eca`).

Kod aplikacji jest taki sam na lokalnym `develop` (`e3e2ae9`) i na `origin/master`. Gałęzie różnią się tylko plikami `ci.yml`, `release.yml` i `README.md`. Ten plan zastępuje poprzednią wersję `docs/issue-7-plan.md` oraz oba plany cząstkowe (harmonogramy w binarce i konsola TCP).

Etykiety PR: S1..S7 to PR wydania 1.2.0, a T1..T3 to PR wydania 1.3.0. Nazwy zmieniłem, żeby nie myliły się z PR #6 na GitHubie.

## Rozstrzygnięcia po wersji 2 (wiążące, mają pierwszeństwo przed resztą planu)

Stan gałęzi: PR #6 jest zmergowany, `develop` przesunięty fast-forward na `master` (`bdd478f`). Prace idą na gałęzi `feature/issue-7-schedules` (S1..S7), a potem `feature/issue-7-tcp` (T1..T3) odbitej od niej. Numery linii w planie liczono przed PR #6: w `ui.html`, `main.go` i testach są aktualne, w README trzeba je sprawdzić.

**Pytanie 1: A.** Sieć jest zaufana. Bez hasła dostępu; README ostrzega, że przy `-listen 0.0.0.0` wszystko, łącznie z wartościami zmiennych i logiem TCP, jest dostępne dla całej sieci.

**Pytanie 2: A.** Bez własnego resolvera mDNS. Zostaje hint w UI przy `no such host` dla nazwy `.local` na Linuksie i wpis w README.

**Pytanie 3: B.** Konflikt zapisu kończy się komunikatem i przeładowaniem zamiast scalania trójstronnego. To zastępuje sekcję 3.4 i zmienia F2, F4 i S1:
- Go (3.3) bez zmian: ETag ze skrótu treści, `If-None-Match` daje 304, `PUT` wymaga `X-Simple-Rest-Version` (409) i `If-Match` (428), niezgodność daje 412. Ciało 412 to `{"error": "..."}` bez dokumentu.
- UI po 412: zapis automatyczny się zatrzymuje, a `#conflictBanner` (wzorzec `#staleBanner`) pokazuje `Your last change was not saved: the data was changed in another tab or on another computer.` z przyciskiem `Reload`. `Reload` przeładowuje stronę. Do tego czasu karta działa, ale nic nie zapisuje.
- Synchronizacja (F4) zostaje i ogranicza konflikty: gdy karta nie ma zaległego zapisu (`saveTimer` pusty, PUT nie w locie), `GET /api/state` z `If-None-Match: lastTag` co 10 s w widocznej karcie oraz przy `visibilitychange`, `focus` i zamknięciu modala. Odpowiedź 200 przechodzi przez `applyState(doc, {keepEnv: true})` bez scalania, bo lokalnie nie ma niezapisanych zmian stanu. Reguły odraczania przy fokusie w modalu oraz zachowanie edytora zapytania (czysty wczytuje nową wersję, brudny zostaje) są jak w F4.
- Odpadają: `mergeState`, `reconcile`, toasty konfliktu, tabela jednostek scalania i jej testy. `normalizeDoc`, zachowanie nieznanych kluczy (F1), `409` dla starej karty (F3), zmienne (F5), loader testów (F6) i środowisko karty (F7) zostają.
- Szacunek S1 spada z 2,5 do około 1,5 dnia.

## 0. Co się zmieniło względem poprzedniej wersji planu

| Zmiana | Powód |
|---|---|
| Harmonogram działa w goroutine binarki, a nie w karcie przeglądarki. Odpadają: lider kart (`navigator.locks`), BroadcastChannel, `persistAuto`, `applySchedMessage`, `mergeRuntime`, tryb Follow, regex w JS z limitem 1 s, harmonogramy w trybie bez binarki i hint o CORS | Odp. 3: harmonogram nie zależy od otwartej karty |
| Stan włączenia, terminy i blokady istnieją tylko w pamięci procesu. W pliku nie ma `enabled`, `blocked`, `nextAt` ani `settings.schedulesPaused`. `Turn all off` zastępuje `Pause all` | Odp. 3: po każdym starcie, także po aktualizacji, wszystko jest wyłączone |
| Wartość ustawiona przez harmonogram trafia do nakładki w pamięci binarki (`varStore`), a nie do wierszy środowiska w pliku. Binarka nigdy nie zapisuje pliku danych | Odp. 3: znika wyścig z `PUT /api/state`, token nie trafia na dysk, a po restarcie i tak byłby nieważny |
| Budowanie zapytania ma drugą implementację w Go (`effective.go`), sprawdzaną wspólnymi wektorami testowymi JS i Go | Odp. 3: binarka bez karty musi sama zbudować zapytanie |
| Zmienne w komunikatach TCP podstawia binarka (plik plus nakładka). Odpadają `ensureFreshFor`, `ensureStateFresh` oraz kontrakty K1 i K2 | Odp. 3 i 5: jedno źródło zmiennych dla HTTP i TCP, także przy pracy z innego komputera |
| Konsola TCP działa tylko jako klient. Warunkowy PR z nasłuchem znika | Odp. 1 |
| Ramkowanie w 1.3.0: presety None, LF, CRLF, MLLP i STX/ETX oraz Wait for reply. Domyślnie None, a preset wybiera się w definicji połączenia | Odp. 2 |
| Dwa wydania: 1.2.0 z harmonogramami, 1.3.0 z konsolą TCP | Odp. 4 |
| Odpadają `localOnly`, `sameSite`, filtr tematów i baner „TCP works only on this computer”. Cele HTTP i TCP nie mają filtra adresów | Odp. 5 |
| Zapis pliku idzie z `If-Match` (ETag to skrót treści). Konflikt daje 412 z dokumentem, a UI scala zmiany trójstronnie już w 1.2.0. Środowisko jest stanem karty | Odp. 5: praca z kilku komputerów naraz to normalny scenariusz |
| Reguła Host przy nasłuchu poza loopback i zakaz osadzania strony w ramce wchodzą do 1.2.0 jednym PR. Flaga `-allow-host` przyjmuje też `*` | Odp. 5: wejście po IP i po nazwach lokalnych działa bez flag. DNS rebinding przy harmonogramach i TCP ma poważniejsze skutki |
| 1.3.0 ma jeden strumień SSE na każdą widoczną kartę: dane TCP plus sygnały `state` i `sched`. 1.2.0 zostaje przy odpytywaniu | Spójność obu funkcji i limit 6 połączeń HTTP/1.1 |
| Jedno zamykanie procesu, `a.shutdown()`, z obsługą SIGTERM | Odp. 3: zamknięcie ani restart nie mogą zostawić zapytania w locie |
| Pytania 1..5 ze starej wersji są rozstrzygnięte. Zostają 2 pytania, które nie blokują startu prac | Odp. 1..5 |
| Szacunek: 1.2.0 rośnie z 6,5 do około 11 dni, a 1.3.0 z przedziału od 5,5 do 8 do około 9 dni | Harmonogram w Go, praca z wielu komputerów, ramkowanie w MVP |

## 1. Streszczenie

1. Issue #7 wchodzi w dwóch wydaniach. W 1.2.0 dochodzą harmonogramy (`Schedules`): proces simple-rest co zadany czas wysyła zapisane zapytanie i ustawia zmienną z odpowiedzi, np. `{{token}}`. W 1.3.0 dochodzi konsola TCP: klient z ramkowaniem MLLP, STX/ETX, LF i CRLF oraz z Wait for reply.
2. Harmonogramy działają bez otwartej karty, ale tylko wtedy, gdy działa program. Po każdym jego starcie, także po aktualizacji, są wyłączone, bo stan włączenia i ustawione wartości żyją wyłącznie w pamięci binarki.
3. Ręczny Send HTTP, cURL i konsola TCP biorą zmienne z jednego źródła: plik plus nakładka z harmonogramów. Przed wysyłką UI woła `POST /api/vars/ensure`, a binarka wtedy odświeża zaległy token.
4. Przy `-listen 0.0.0.0` interfejs, harmonogramy i konsola działają z każdego komputera w sieci, a cele HTTP i TCP mogą być dowolnymi hostami osiągalnymi z maszyny z binarką. Dlatego zapis pliku dostaje `If-Match` ze scalaniem, środowisko staje się stanem karty, a wejście chroni reguła Host.
5. Pracę poprzedzają rozstrzygnięcie PR #6 i fast-forward `develop`. Potem jest siedem PR do 1.2.0 (S1..S7, około 11 dni) i trzy do 1.3.0 (T1..T3, około 9 dni). Każdy PR ma testy i jest weryfikowany lokalnie, bo Actions stoją na limicie.

## 2. Odpowiedzi użytkownika i ich interpretacja

| Nr | Odpowiedź | Interpretacja w planie |
|---|---|---|
| 1 | A | simple-rest łączy się do `host:port` urządzenia lub systemu. Nasłuch jest poza zakresem 1.3.0 i nie ma go w „Później” |
| 2 | A, „nie mam pojęcia” | Presety None, LF, CRLF, MLLP (`0B ... 1C 0D`) i STX/ETX z dzieleniem odbioru na komunikaty oraz Wait for reply wchodzą do 1.3.0. Domyślny preset to None (surowe bajty), bo protokół jest nieznany. Preset jest polem definicji połączenia, a jego zmiana działa po Reconnect |
| 3 | Harmonogram niezależny od karty, działa tylko przy uruchomionej aplikacji, po uruchomieniu wyłączony | „Aplikacja” to proces simple-rest, a nie przeglądarka. Wyłączenie obowiązuje przy każdym starcie procesu: ręcznym, po aktualizacji (na Windows nowy proces, na Linuksie `exec` z tym samym PID) i po awarii. Żadna flaga ani ustawienie tego nie zmienia. Karty wykrywają restart po polu `boot` i pokazują toast |
| 4 | A | 1.2.0 wychodzi pierwsze. PR T1..T3 trafiają do `develop` dopiero po opublikowaniu 1.2.0 |
| 5 | „Urządzenia mogą nie być tylko lokalnie, musimy obsługiwać wszystko z tej samej sieci” | Dwa odczytania, oba obsłużone. Szczegóły niżej |

**Odp. 5, odczytanie (a): cele w całej sieci**
- Cel zapytania HTTP, harmonogramu albo połączenia TCP może być dowolnym adresem osiągalnym z maszyny, na której działa binarka. Nie musi być osiągalny z komputera z przeglądarką. README ma to powiedzieć wprost dla Raspberry Pi.
- Nie ma filtra zakresów adresów, bo sieci firmowe często używają adresów spoza RFC 1918.
- Działa IPv6, także ze strefą (`fe80::1%eth0`), oraz nazwy DNS.
- Ograniczenie: binarki linuksowe nie rozwiązują nazw `.local` (mDNS).
  - Budujemy je z `CGO_ENABLED=0` (build.sh:18).
  - W Go 1.26 `net/conf.go` bez cgo pomija źródło `mdns` z nsswitch i pyta zwykły DNS. Sprawdzone w źródle: gałąź `mdns` stoi pod `if canUseCgo`.
  - Na Windows Go woła `GetAddrInfoW` (`net/lookup_windows.go:129`), więc `.local` działa tak jak w systemie.
  - W planie: hint w UI i wpis w README już w 1.2.0. Własny resolver to pytanie 2.

**Odp. 5, odczytanie (b): UI z innego komputera**
- Przy `-listen 0.0.0.0` (README:20..22) całe UI, harmonogramy, konsola TCP i strumień zdarzeń działają z każdej przeglądarki w sieci, przez ten sam `guard` co dziś.
- Skutki:
  - Kilka komputerów zapisuje ten sam plik, więc zapis idzie z `If-Match`, a UI scala zmiany (3.3, 3.4).
  - Każda karta ma własne aktywne środowisko (3.2, F7).
  - Harmonogram włączony z komputera B działa dalej po zamknięciu karty na B. Komputer A widzi `from 192.168.1.20`.
  - Sesja TCP jest wspólna dla wszystkich klientów i ma własne środowisko, ustalone przy Connect (5.7).
  - Reguła Host przepuszcza IP i nazwy lokalne bez flag. Inne nazwy wymagają `-allow-host`, a `-allow-host '*'` przywraca dzisiejsze zachowanie (3.6).
  - Uwierzytelniania nie ma, jak dziś (pytanie 1).
- Poza zakresem: dostęp spoza sieci lokalnej (NAT, internet) i TLS dla UI.

## 3. Wspólne fundamenty

### 3.1 Słowniczek

- UI: `Schedules`, `TCP`, `Environments`.
- README, CHANGELOG, strona i komentarz w issue: „harmonogram (kanał cykliczny)”, „konsola TCP”, „połączenie TCP”, „nakładka” (wartości z harmonogramów).
- Słowo „channel” nie pojawia się w UI.

### 3.2 Stan w UI (S1)

**F1. `normalizeDoc` i `applyState`**
- `normalizeDoc(data)` to czysta funkcja, która zwraca znormalizowany dokument:
  - `settings` łączone z `DEFAULT_SETTINGS` (L359);
  - kolekcje i środowiska przez `Object.assign(domyślne, x)`, więc nieznane pola przeżywają (dziś L418..422 je gubią);
  - zapytania przez `normalizeRequest`;
  - harmonogramy przez `normalizeSchedule` (S6), połączenia TCP przez `normalizeTcp` (T2);
  - nieznane klucze najwyższego poziomu trafiają do `extra`.
- `applyState(doc, { keepEnv })` kopiuje dokument do `state` w miejscu. `state` nigdy nie jest podmieniany, więc `__sr.state` zachowuje referencję.
- `serializeState()` = `JSON.stringify(Object.assign({}, extraKeys, state))`.
- `loadState` czyta tekst, ustawia `lastSynced` i `lastTag`, a potem woła `applyState`.
- Normalizacja typów (dziś import w L903 robi `Object.assign(newRow(), v)` bez konwersji):

| Pole | Reguła |
|---|---|
| teksty: `name` i `value` w wierszach, `url`, `body.raw`, pola `auth` | `str(v)`: string bez zmian; `null` albo brak daje `''`; liczba i bool przez `String(v)`; obiekt przez `JSON.stringify` |
| `enabled` w wierszu | brak daje `true`, inaczej `!!v` |
| `options.timeoutMs`, `settings.timeoutMs` | `Math.max(0, Math.trunc(+v) \|\| 0)` |
| `followRedirects`, `verifyTls`, `cookies` | `!!v` |
| `method` | `String(v \|\| 'GET').toUpperCase()`, jak dziś |
| `body.rawType` | własny klucz `RAW_TYPES` (`Object.hasOwn`), inaczej `'text'` |

**F2. Zapis (`persistNow`, L399..409)**
- W locie jest najwyżej jeden PUT. Zmiana w trakcie ustawia `again`.
- Nagłówki: `X-Simple-Rest-Version: <info.version>` i `If-Match: <lastTag>`.
- Odpowiedzi:
  - 204: `lastTag` = ETag z odpowiedzi, `lastSynced` = wysłane body;
  - 409: `stale`;
  - 412: `reconcile(dokument, ETag)` (3.4), potem ponowny PUT, najwyżej 3 razy. Potem toast `Could not save: the data file keeps changing. Try again.`;
  - błąd sieci: `unsaved`, ponowienie co 5 s, toast tylko przy pierwszej porażce z serii.
- `flushPersist()` kasuje `saveTimer` i czeka na zapis.
- Tryb bez binarki zapisuje do `localStorage` jak dziś.

**F3. Karta nieaktualna**
- `PUT /api/state` z inną wersją albo bez nagłówka dostaje 409 z treścią `This page comes from another simple-rest version than the running program (1.2.0). Reload the page.`
- Treść jest zwykłym tekstem, bo karta 1.1.0 pokazuje ją w toaście `Save failed: …` (L404..405).
- Karta 1.2.0 po 409 ustawia `stale`, pokazuje `#staleBanner` z przyciskiem `Reload` i wstrzymuje autozapis, akcje harmonogramów, `ensure` oraz akcje TCP (3.10).
- Wcześniejsze wykrycie: `GET /api/info` po `visibilitychange` oraz pole `version` w snapshocie harmonogramów.
- `installUpdate` (L1008) najpierw robi `await persistNow()` i nie instaluje, jeśli zapis się nie udał.

**F4. Synchronizacja (`syncIfBehind(hintTag)`)**
- Kroki:
  1. Gdy `hintTag === lastTag`, nic nie robi.
  2. Zaległy zapis (`saveTimer` albo PUT w locie) wypycha przez `flushPersist()`. Ewentualny konflikt kończy się 412 i scaleniem.
  3. Gdy fokus jest w polu modala albo formularza TCP, którego wartość różni się od modelu, odkłada synchronizację do `focusout`.
  4. W pozostałych przypadkach wysyła `GET /api/state` z `If-None-Match: lastTag`. 304 nic nie zmienia, a 200 przechodzi przez `reconcile`.
- Wyzwalacze:
  - S1: timer co 10 s w widocznej karcie, `visibilitychange`, `focus`, zamknięcie modala;
  - S6: `stateTag` ze snapshotu harmonogramów (bez dodatkowego GET, gdy tag jest ten sam);
  - T2: zdarzenie `state.changed`;
  - tryb bez binarki: zdarzenie `storage`.
- Po synchronizacji UI przerysowuje sidebar, historię, `#envSelect`, otwarte `#envModal` i `#schedModal` oraz `#tcpView`. Odtwarza fokus i kursor: tabela, wiersz, kolumna, zaznaczenie.
- Edytor zapytania:
  - czysty edytor z zapytaniem zmienionym w pliku wczytuje nową wersję;
  - edytor z niezapisanymi zmianami zostaje, a `savedSnapshot` dostaje wersję z pliku, więc `#dirty` pokazuje różnicę;
  - zapytanie usunięte gdzie indziej daje `curCollId = null`, jak w L772.

**F5. Zmienne**
- Dochodzą `findEnv(id)` i `varsOf(env)`.
- `varsOf` zwraca mapę `Object.create(null)` z włączonych wierszy o niepustej nazwie. Przy duplikatach wygrywa ostatni.
- `subst` sprawdza `Object.hasOwn`, więc `{{constructor}}` przestaje się rozwijać. Dziś rozwija się przez `k in vars` w L457.
- `effectiveVars(envId)` zwraca zmienne środowiska nadpisane nakładką (3.5). `activeVars()` = `effectiveVars(state.activeEnv)`.
- `buildEffective(r, vars = activeVars())` liczy timeout przez `Math.trunc`.

**F6. Testy i zasada startu**
- `tests/load-ui.mjs` leży poza globem `*.test.mjs`. Wyciąga skrypt tak jak dziś tests/ui.test.mjs L10..13 i eksportuje `sr`.
- Timery, listenery i EventSource powstają wyłącznie w `init()`. Inaczej `node --test` się zawiesza, a w CI czeka do `timeout-minutes: 15`.

**F7. Środowisko karty**
- `activeEnv` przy starcie bierze się z `sessionStorage['simple-rest-env']`. Gdy go tam nie ma albo takie środowisko nie istnieje, bierze się z pliku.
- Wybór w `#envSelect` (L1102) ustawia `sessionStorage` i robi `persist`. Plik dostaje wtedy tylko wartość startową dla nowej karty.
- Scalanie zostawia `activeEnv` karty.
- Dziś `loadState` bierze `activeEnv` z pliku (L423). Bez tej zmiany synchronizacja przestawiałaby środowisko innym komputerom.

**F8. Import i eksport (S2)**
- `exportData()` i `importData(d)` są czyste i bez toastów.
- `importData` zwraca `{collections, envs, schedules, tcp, skipped: []}`.
- Własny format z nieznanym kluczem najwyższego poziomu (np. `tcp` w 1.2.0) dopisuje go do `skipped`. Toast w `importFiles` mówi wtedy `Skipped: tcp (needs a newer simple-rest)`, zamiast `Unrecognized format` z L909.

### 3.3 Rewizja pliku danych (S1, Go)

- **ETag** to `"<16 hex>"`, czyli pierwsze 16 znaków hex SHA-256 bajtów, które oddaje `GET /api/state`. Dla braku pliku liczy się z `null`.
- Taki tag przeżywa restart, więc po aktualizacji karty nie dostają fałszywego 412. Wykrywa też ręczną edycję pliku.
- **Pola `app`:**
  - `rev uint64`: licznik wewnętrzny, +1 przy każdej zmianie tagu;
  - `tag string`;
  - `stamp`: mtime i rozmiar przy ostatnim odczycie lub zapisie.

  Wszystkie trzy chroni `fileMu`.
- **Funkcje:**
  - `a.readState()` czyta plik i liczy tag pod `fileMu`;
  - `a.currentTag()` robi tylko `stat`, a przy niezmienionym `stamp` zwraca tag z pamięci. Używają go odpytywanie i scheduler.
- **`GET /api/state`:** 200 z `ETag`. Pasujące `If-None-Match` daje 304.
- **`PUT /api/state`**, w tej kolejności:
  1. `X-Simple-Rest-Version` jest sprawdzany przed czytaniem ciała. Brak albo inna wartość daje 409 (F3).
  2. Brak `If-Match` daje 428 `Missing If-Match. Reload the page.`
  3. Ciało do 64 MB musi przejść `json.Valid`, inaczej 400 (jak dziś).
  4. Pod `fileMu` handler czyta plik i liczy tag zawsze, bez skrótu przez `stamp`. Niezgodność daje 412 z bieżącym dokumentem w ciele i jego `ETag`, a plik zostaje bez zmian.
  5. Zapis przez `.tmp` i `rename`, jak dziś. Potem `tag` = skrót body, `rev++`, nowy `stamp`.
  6. Po zwolnieniu `fileMu` idzie hook `a.onStateChange(rev, tag)`: S5 podpina `sched.stateChanged`, T1 publikuje `state.changed`.
  7. Odpowiedź 204 z `ETag`.
- Błędy `/api/state` zostają zwykłym tekstem, jak dziś w handleState (main.go:263..303).
- Zewnętrzna zmiana pliku jest wykrywana przez `stat` przy GET, przy odpytywaniu i w pętli schedulera co 5 s. Daje `rev++` i hook.
- Edycja o tym samym rozmiarze w obrębie rozdzielczości mtime umknie odpytywaniu, ale nie PUT, bo PUT zawsze liczy skrót.
- Żaden skrypt w `scripts/` nie woła `/api/state` (sprawdzone grepem).

### 3.4 Scalanie trójstronne (S1, JS)

`reconcile(theirsText, tag)` działa tak:
1. `base = normalizeDoc(JSON.parse(lastSynced))`;
2. `mine` to bieżący stan;
3. `theirs = normalizeDoc(JSON.parse(theirsText))`;
4. `{doc, conflicts} = mergeState(base, mine, theirs)`;
5. `applyState(doc, {keepEnv: true})`, potem `lastSynced = theirsText` i `lastTag = tag`;
6. gdy `doc` różni się od `theirs`, idzie `persist`.

Bez lokalnych zmian (`mine` równe `base`) wynikiem jest po prostu `theirs`. Ta sama ścieżka obsługuje 412, synchronizację i zdarzenie `storage`.

| Część | Klucz | Jednostka scalania |
|---|---|---|
| `settings`, nieznane klucze najwyższego poziomu | nazwa | wartość klucza |
| kolekcje | `id` | pola `name` i `open` |
| zapytania w kolekcji | `id` | całe zapytanie |
| środowiska | `id` | `name` oraz cała lista `vars` |
| historia | `id` | wpis; wynik to suma, sortowanie po `at` malejąco, limit 200 |
| `schedules` (S6), `tcp` (T2) | `id` | cała definicja, razem z `messages` |
| `activeEnv` | | zawsze z karty |

Reguły:
- Jednostka bierze wersję „mine”, gdy zmieniła się względem `base`. W przeciwnym razie bierze „theirs”.
- Usunięcie po jednej stronie wygrywa, chyba że druga strona tę jednostkę zmieniła. Wtedy zmieniona wersja zostaje, żeby nie zgubić pracy.
- Kolejność pochodzi z „theirs”. Elementy tylko z „mine” wstawiane są za swoim poprzednikiem, a gdy go nie ma, na końcu.
- Konflikt to ta sama jednostka zmieniona po obu stronach albo zmiana kontra usunięcie. Konflikt daje jeden toast:
  - `Also changed on another computer: "Login". Your version was kept.`;
  - `"Login" was deleted on another computer, but you changed it, so it was kept.`
- Dla `tcp` do czasu T2 działa reguła klucza nieznanego, czyli cała tablica jako jedna jednostka.

### 3.5 Zmienne: jedno źródło (S1, S4, S5, T1)

Pierwszeństwo jest takie samo w JS i w Go:
1. Wartość z nakładki dla pary (envId, nazwa). Działa także dla nazwy, której nie ma w tabeli, i dla wiersza wyłączonego.
2. Włączony wiersz środowiska z niepustą nazwą. Nazwa nie jest przycinana, a przy duplikatach wygrywa ostatni wiersz.
3. Brak wartości:
   - ręczny Send HTTP i komunikat TCP zostawiają `{{x}}` dosłownie, jak dziś (test w tests/ui.test.mjs L46). TCP dodatkowo zgłasza brak w polu `missing`;
   - harmonogram nic nie wysyła (klasa `config`).

Kto podstawia:

| Ścieżka | Kto podstawia | Świeżość |
|---|---|---|
| Ręczny Send HTTP i cURL | UI, `buildEffective(cur, effectiveVars(envId karty))` | `flushPersist()`, potem `ensureVars(names, envId)` |
| Harmonogram | binarka, `effective.go` z pliku i nakładki | wartości z własnej pamięci |
| Connect i Send TCP (1.3.0) | binarka, `effectiveVars(envId sesji)` | UI woła `ensureVars` przed wysyłką; handler TCP nie trzyma HTTP do 20 s |
| Podgląd `Sends:` i licznik bajtów | UI, ta sama funkcja z kopią nakładki (`ovl`) | odpytywanie (1.2.0) albo SSE (1.3.0) |

### 3.6 Dostęp z sieci (S3)

**Reguła Host**
- Przy nasłuchu na loopback zachowanie zostaje jak dziś (main.go:321).
- Przy nasłuchu poza loopback dziś działa każda nazwa. Po S3 `hostAllowed(h)` przyjmuje, po `ToLower` i zdjęciu końcowej kropki:
  - literał IP, także IPv6 w nawiasach i ze strefą;
  - `localhost`;
  - nazwy jednoczłonowe (bez kropki), np. `raspberrypi`;
  - sufiksy, których nie da się zarejestrować w publicznym DNS: `.local`, `.lan`, `.home`, `.home.arpa`, `.internal`, `.localdomain`;
  - `os.Hostname()`, z sufiksem i bez;
  - nazwy z `-allow-host a,b`.
- `-allow-host '*'` wyłącza test, czyli przywraca dzisiejsze zachowanie, i loguje przy starcie `Host check is off (-allow-host *): a web page can reach simple-rest through DNS rebinding.`
- Odrzucenie daje 403 `forbidden host "pc123.firma.pl": open simple-rest by its IP address or start it with -allow-host pc123.firma.pl`.
- Pierwsze odrzucenie danej nazwy trafia do konsoli z gotową flagą.
- Linia startowa (main.go:486..488) wymienia dozwolone sposoby wejścia.

**Pozostałe elementy S3**
- `handleIndex` (main.go:233..241) dostaje `X-Frame-Options: DENY` i `Content-Security-Policy: frame-ancestors 'none'`. CSP ma tylko tę dyrektywę, bo `ui.html` ma skrypt i style inline.
- Podgląd HTML odpowiedzi to `iframe` z `srcdoc` (L714), ramka potomna tej strony, więc nagłówki go nie dotyczą. Sprawdzenie jest w checkliście.
- `pollUpdate` (L1018..1037) rozpoznaje 403 po restarcie. Pokazuje wtedy `simple-rest restarted but refuses the address you opened it with. Open it by its IP address or start it with -allow-host.` zamiast `did not come back` (L1031..1033).
- Restart zachowuje argumenty (main.go:410..413).

### 3.7 Konwencje API i logu (S3, S5, T1)

- **Trasy:**
  - wzorce bez metody i ręczne 405 z nagłówkiem `Allow`, bo catch-all `/` (main.go:339) zamieniłby niedopasowaną metodę na 404;
  - `{id}` czyta się przez `r.PathValue`, co działa dzięki `go 1.26` w go.mod.
- **Helpery:**
  - `jsonError(w, status, msg)` zwraca `{"error": msg}`, jak update.go:686;
  - `methodNotAllowed(w, allow...)`.

  Istniejące trasy (`/api/send`, `/api/state`, `/api/cookies`, `/api/update*`) zostają przy dzisiejszych odpowiedziach.
- **Walidacja ścieżki:**
  - `{id}` i `{envId}` muszą pasować do `^[A-Za-z0-9_-]{1,64}$`;
  - `{name}` musi pasować do `^[A-Za-z0-9_.-]{1,128}$`, czyli klasy z `subst`;
  - inna wartość daje 400.
- **POST z ciałem:**
  - wymaga `Content-Type: application/json` (`mime.ParseMediaType`), inaczej 415;
  - `http.MaxBytesReader` ogranicza ciało do 64 KB dla `/api/schedules*` i `/api/vars*` oraz do 2 MB dla `/api/tcp*`, inaczej 413;
  - akcje bez danych wysyłają `{}`.
- **503** w czasie zamykania ma jedną treść dla wszystkich nowych tras: `{"error":"simple-rest is shutting down"}`.
- **Log:**
  - `logq.go` (S3): kanał 64 linii, jedna goroutine woła `log.Print`;
  - pełny kanał oznacza odrzucenie linii, a przy następnym zapisie pojawia się `(n log lines dropped)`;
  - handlery, guard, scheduler i TCP piszą tylko tędy i nigdy pod blokadą. Powód: QuickEdit w conhost i XOFF w terminalu wstrzymują zapis, a `log.Logger` serializuje zapisy mutexem;
  - updater zostaje przy `log.Printf`, bo e2e szuka jego linii przed wyjściem procesu (scripts/e2e-update.sh:111);
  - `a.shutdown()` opróżnia kolejkę, czekając najwyżej 200 ms.
- **Linie konsoli:**
  - akcje na harmonogramach mają adres nadawcy, np. `schedule "Token refresh" on (from 192.168.1.20)`;
  - Connect i Disconnect TCP też, np. `tcp "Lab analyzer" connected to 10.0.0.5:2575 (from 192.168.1.30)`;
  - w logu nie ma treści komunikatów, wartości zmiennych ani URL.

### 3.8 Cykl życia procesu (S5, T1)

- `a.boot` to 16 znaków hex z `crypto/rand`, nowe przy każdym starcie (S5). Pojawia się w `/api/info`, w snapshocie harmonogramów i jako prefiks kursora SSE `<boot>.<seq>` (T1).
- `a.shutdown()` jest idempotentne (`sync.Once`):
  1. ustawia `closing` (`atomic.Bool`), więc nowe trasy dostają 503;
  2. `sched.stop()`;
  3. `tcp.close()` (T1);
  4. `bus.close()` (T1);
  5. czeka na goroutines najwyżej 1 s łącznie;
  6. opróżnia `logq`.
- Kto woła `a.shutdown()`:
  - `u.restart = func() { a.shutdown(); restartProcess(srv, exePath) }` (main.go:468). Wywołanie jest synchroniczne, bo `RegisterOnShutdown` uruchamia funkcje w goroutines i na nie nie czeka;
  - `srv.RegisterOnShutdown(a.shutdown)`;
  - goroutine sygnałów: `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)` (main.go:470). Według `go doc os/signal` Windows dostarcza CTRL_CLOSE_EVENT jako `syscall.SIGTERM`, a na Linuksie SIGTERM wysyła systemd.
- `main` po powrocie z `Serve` czeka do 3 s na zakończenie goroutine zamykającej. Dziś wychodzi od razu (main.go:492..497), więc `Shutdown` nie ma szansy zadziałać.
- Po każdym starcie wszystkie harmonogramy są wyłączone, nakładka pusta, sesji TCP brak, a konsola dostaje linię `schedules: 3 defined, all off until turned on`.
- `installUpdate` pyta jednym tekstem złożonym z części: `Installing restarts the program: 2 schedule(s) turn off and 1 TCP connection(s) close. Continue?`. Część o TCP dochodzi w T2.

### 3.9 Aktualizacje stanu w kartach

**1.2.0 (S6): odpytywanie**
- Jeden `setInterval(tick, 2000)` w `init()`.
- Tick woła `GET /api/schedules?since=<boot>.<gen>`, który przy braku zmian odpowiada 204. Przychodzący `stateTag` przekazuje do `syncIfBehind`.
- Częstotliwość:
  - co 2 s przy otwartym modalu `Schedules` albo `Environments`;
  - co 10 s w widocznej karcie;
  - karta w tle wcale;
  - od razu po `visibilitychange` i po każdej akcji.

**1.3.0 (T1, T2): jeden EventSource na każdą widoczną kartę w trybie server**

| Temat | Zdarzenia | Reakcja karty |
|---|---|---|
| `tcp` | `tcp.snapshot`, `tcp.state`, `tcp.entry`, `tcp.cleared`, `tcp.removed` | `tcpReduce` |
| `state` | `state.changed {tag}` | `syncIfBehind(tag)` |
| `sched` | `sched.changed {gen}`, łączone do najwyżej jednego na 250 ms | `GET /api/schedules?since=` |
| | `ready` (bez `id`) po replayu lub migawce | `live = true` |

- Przy `live` karta nie odpytuje. Bez `live` wraca do odpytywania z 1.2.0.
- Karta schowana zamyka strumień i ustawia `live = false`. Po powrocie otwiera go z `?after=<boot>.<seq>`.
- Po odpowiedzi innej niż 200 (np. 503 w czasie restartu) karta ponawia otwarcie:
  - co 2 s przez 30 s;
  - potem co 30 s;
  - przy `focus`, `online` i `visibilitychange`.
- Ograniczenie: serwer działa po HTTP/1.1 bez TLS (main.go:467), więc przeglądarka trzyma najwyżej 6 połączeń na host, wspólnie dla wszystkich kart. Jedna widoczna karta zajmuje jedno połączenie, a czekanie na odpowiedź TCP nie trzyma żadnego.
- Restart wykrywa zmiana `boot` w snapshocie albo w SSE. Daje ona toast `simple-rest was restarted: all schedules are off. Turn them on in Schedules.`, o ile przed restartem był włączony harmonogram albo nakładka miała wartości.

### 3.10 Tryb bez binarki i karta nieaktualna

Wzorzec jest jeden dla obu funkcji: funkcja jest widoczna, ma baner, a akcje są wyłączone.
- `featureState()` zwraca `ok`, `nobin` albo `stale`.
- Teksty:
  - Schedules: `Schedules run inside the simple-rest program. Open this page from the program to use them.`;
  - TCP: `TCP needs the simple-rest program: browsers cannot open raw TCP sockets.`;
  - przy `stale`: `This page comes from another simple-rest version than the running program. Reload the page.`
- Definicje zaimportowane bez binarki są widoczne w modalu tylko do odczytu.
- Przy `stale` zablokowane są: `Enabled`, `Run now`, `Delete`, `Turn all off`, `ensure`, Connect, Send i Disconnect TCP. Ręczny Send HTTP działa.

### 3.11 Testy wspólne

- Loader `tests/load-ui.mjs` (S1).
- Wspólne wektory:
  - `testdata/effective.json`: budowanie zapytania, JS i Go (S4);
  - `testdata/tcp-framing.json`: ramkowanie, JS i Go (T1).
- `scripts/e2e-update.sh` po restarcie sprawdza:
  - brak `"enabled":true` i brak wartości w nakładce (S5);
  - `sessions: []` (T1);
  - niezmieniony plik danych.

## 4. Funkcja 1: harmonogramy (1.2.0)

### 4.1 Cel i zakres

**Cel.** Harmonogram (w UI `Schedules`):
- co zadany czas wysyła zapisane zapytanie, np. logowanie;
- wyciąga wartość z odpowiedzi;
- ustawia nią zmienną, np. `{{token}}`, w nakładce wybranego środowiska.

Środowiska i `{{nazwa}}` już istnieją (L451..457, L570..599).

**MVP:**
- definicje w pliku, stan w pamięci binarki;
- reguły pobierania wartości: JSON, nagłówek, regex, flaga `optional`;
- termin z `everySec` oraz z `expires_in` minus `leadSec`;
- ochrona konta przed blokadą;
- `ensure` przed ręcznym Send i cURL;
- modal, znaczniki w sidebarze, sekcja nakładki w Environments;
- eksport i import definicji.

**Później:**
- ekran konfliktu dla tego samego pola;
- termin z `exp` w JWT;
- zależności między harmonogramami;
- reguły Capture przy ręcznym Send;
- ścieżki JSON z kropką w kluczu;
- hasło dostępu, jeśli pytanie 1 nie przesunie go do 1.2.0.

### 4.2 Architektura i przepływ

```
 przeglądarka (komputer A lub B)                          binarka (komputer A albo Raspberry Pi)
 ui.html  state, activeEnv (karta), ovl (kopia nakładki)
   PUT /api/state  If-Match + X-Simple-Rest-Version  ---> handleState --tmp+rename--> simple-rest-data.json
   GET /api/schedules?since=<boot>.<gen>  (2 s / 10 s) <--- scheduler (jobs, gen)          ^ odczyt pod fileMu,
   POST /api/schedules/{id}/enable|disable|run       ---> scheduler.run ---a.exchange-------|- cache po tagu
   POST /api/vars/ensure (przed Send i cURL)         ---> varStore (nakładka)              v
   POST /api/send (buildEffective z ovl)             ---> a.send ------------> API w sieci   serwer tokenu w sieci
```

| Dane | Gdzie leżą | Kto zapisuje | Czas życia |
|---|---|---|---|
| definicje harmonogramów, zapytania, środowiska | plik danych | tylko UI, przez `PUT /api/state` | trwałe |
| `activeEnv` | `sessionStorage` karty; w pliku tylko wartość startowa | karta | do zamknięcia karty |
| `enabled`, `blocked`, terminy, log | pamięć binarki | binarka, na polecenie z UI | do końca procesu |
| nakładka | pamięć binarki (`varStore`) | binarka | do `Clear` albo do końca procesu |
| `ovl`, `schedSnap`, `skew` | pamięć karty | karta | do przeładowania |

**Nowe pliki Go:**
- `effective.go` (S4);
- `extract.go`: reguły, klasyfikacja, terminy;
- `schedule.go`: `scheduler`, `varStore`, widok pliku z cache, handlery.

**Zmiany w `main.go`:**
- `a.exchange(ctx, in) (exchangeResult, error)` wydzielone z `a.send` (main.go:119..218);
- ten sam transport, proxy, cookie jar i limit 64 MB;
- `exchange` zwraca surowe bajty body i surowy `error`;
- `a.send` opakowuje wynik jak dziś, co pilnują istniejące `TestSend*`.

### 4.3 Model danych

**Plik: nowy klucz `schedules`, wyłącznie definicje**
```json
"schedules": [{
  "id": "k3j2a9x1lq", "name": "Token refresh", "requestId": "abc123mf0", "envId": "e1",
  "everySec": 240, "expiresPath": "expires_in", "leadSec": 60,
  "extract": [
    { "enabled": true,  "optional": false, "var": "token",   "from": "json",   "path": "access_token" },
    { "enabled": true,  "optional": true,  "var": "refresh", "from": "json",   "path": "refresh_token" }
  ]
}]
```

| Pole | Reguła (`normalizeSchedule`) |
|---|---|
| `id` | `^[A-Za-z0-9_-]{1,64}$`; inna wartość dostaje nowe `uid()` |
| `requestId` | pierwsze trafienie we wszystkich kolekcjach |
| `envId` | zawsze konkretne środowisko; trybu Follow nie ma |
| `everySec` / `leadSec` | od 10 do 86400, domyślnie 240 / od 0 do 3600, domyślnie 60 |
| `expiresPath` | ścieżka JSON do liczby sekund; puste pole wyłącza tę funkcję |
| `extract[]` | najwyżej 20 reguł; `from`: `json`, `header` albo `regex` (nieznana wartość daje `json`); `var` musi pasować do `^[A-Za-z0-9_.-]{1,128}$` |

- Go czyta tylko pola z tej tabeli. Dopisane ręcznie `"enabled": true` niczego nie włącza.
- `version` zostaje 1, bo zmiana jest tylko addytywna.

**Pamięć binarki**
```go
type overlayVar struct{ Value, By string; At, ExpiresAt int64 }   // By = id harmonogramu, unix ms
type varStore struct{ mu sync.Mutex; m map[string]map[string]overlayVar } // envId -> nazwa -> wartość
type jobRT struct {
	enabled, enabledLocal bool; enabledAt int64; enabledBy string
	gen uint64; running bool; done chan struct{}; cancel context.CancelFunc
	redo bool; missFiredAt, nextAt, plannedAt, lastRunAt, lastOkAt int64; nextFrom string
	failures int; state string // off|waiting|running|ok|retry|stopped|config
	lastError, blocked, blockedFP string; log []logEntry; snippet string; fields []string
}
type scheduler struct {
	mu sync.Mutex; jobs map[string]*jobRT; gen uint64; rev uint64; stateTag string; stopping bool
	sem chan struct{} /* 4 */; wake chan struct{}; now func() int64
	send func(context.Context, sendRequest) (exchangeResult, error); view *fileView
	ctx context.Context; cancel context.CancelFunc; wg sync.WaitGroup
}
```

**Reguły dostępu do pamięci binarki**
- `fileMu`, `s.mu` i `varStore.mu` nigdy nie są trzymane jednocześnie.
- Każdy start przebiegu sprawdza `stopping` i robi `wg.Add(1)` w tej samej sekcji `s.mu`. Bez tego `sync/waitgroup.go` w Go 1.26 panikuje: `Add called concurrently with Wait` w liniach 121 i 132.
- `s.view()` czyta plik tylko po zmianie tagu (`a.currentTag()`). Dekoduje `settings`, `collections` (zapytania indeksowane po id), `envs` i `schedules`. Historia jest pomijana.

**Pamięć karty** (poza `state`, nie trafia do zapisu ani eksportu)
- `ovl = { boot, vars: { [envId]: { [name]: {value, by, at, expiresAt} } } }`;
- `schedSnap`, `skew = snap.now - Date.now()`;
- `seenTag` = `stateTag` z ostatniego snapshotu.

**Zgodność**
- Plik z 1.1.0 nie ma `schedules`, więc lista jest pusta.
- Karta 1.1.0 po aktualizacji dostaje 409 i nie skasuje `schedules`.
- Cofnięcie do binarki 1.1.0 gubi `schedules` przy pierwszym zapisie (wpis w CHANGELOG).

### 4.4 API

| Metoda i ścieżka | Wejście | Odpowiedź | Błędy |
|---|---|---|---|
| `GET /api/info` | | dochodzi `boot` | |
| `GET /api/schedules[?since=<boot>.<gen>]` | | 200 snapshot; 204, gdy od `since` nic się nie zmieniło. Przed porównaniem `a.currentTag()` | 405 |
| `POST /api/schedules/{id}/enable` | `{}` | 200 job. Czyta plik od razu i ustawia `enabled`, `enabledAt`, `enabledBy`, `blocked=''`, `failures=0`. Bez przebiegu w locie: `gen++`, `nextAt=now`, stan `waiting`. Przy przebiegu w locie `gen` się nie zmienia | 404 `Schedule not found in the data file`, 422 `{error, problems[]}`, 409 `too many schedules on (32)`, 503 |
| `POST /api/schedules/{id}/disable` | `{}` | 200 job, idempotentne. `gen++`, anulowanie przebiegu w locie. Nakładka zostaje | 404 |
| `POST /api/schedules/{id}/run` | `{}` | 200 job, gdy przebieg skończył się w czasie `min(timeout + 2 s, 60 s)`; inaczej 202 ze stanem `running`. Trwający przebieg nie dostaje drugiego strzału. Działa także na harmonogramie wyłączonym albo zatrzymanym. Sukces zdejmuje `blocked` | 404, 422, 503 |
| `POST /api/schedules/disable-all` | `{}` | 200 `{"off": n}` | |
| `POST /api/vars/ensure` | `{"envId","names":[...]}`, najwyżej 256 nazw | 200 (niżej) | 400, 413, 415 |
| `DELETE /api/vars/{envId}/{name}` | | 204 | 404 |

**Snapshot**
```json
{"boot":"9f3ac1d2e4b5a6f7","gen":57,"now":1790000000000,"version":"1.2.0","stateTag":"\"3b1f0c9a7d2e4f60\"",
 "jobs":{"k3j2a9x1lq":{"enabled":true,"state":"ok","enabledAt":1789999990000,"enabledBy":"192.168.1.20",
   "enabledLocal":false,"nextAt":1790000180000,"nextFrom":"expires_in","lastRunAt":1790000000000,
   "lastOkAt":1790000000000,"failures":0,"lastError":"","blocked":"","changedSinceStop":false,"problems":[],
   "fields":["access_token","expires_in"],"snippet":"","log":[{"at":1790000000000,"status":200,"timeMs":148,"note":"token set"}]}},
 "vars":{"e1":{"token":{"value":"eyJ...","by":"k3j2a9x1lq","at":1790000000000,"expiresAt":1790000300000}}}}
```
- `jobs` obejmuje każdą definicję z pliku. Harmonogram, którego nikt nie włączył, ma stan `off`.
- `problems` liczy się przy parsowaniu pliku: brak zapytania lub środowiska, pole złego typu, błędny regex, zła nazwa zmiennej, brak włączonej reguły.
- `changedSinceStop` = `blocked != ''` i odcisk definicji, zapytania oraz środowiska różny od `blockedFP`.
- Atomowość:
  - `gen`, `stateTag` i `jobs` czytane są w jednej sekcji `s.mu`, a `vars` po niej;
  - zapisujący najpierw ustawia nakładkę albo plik, a potem robi `gen++`;
  - `stateChanged(rev, tag)` zapisuje tag tylko przy większym `rev`.
- `lastError`, `log[].note` i notatki nie zawierają URL.

**`ensure`**
```json
{"boot":"9f3ac1d2e4b5a6f7","envId":"e1","vars":{"token":{"value":"eyJ...","by":"k3j2a9x1lq","at":1790000000000,"expiresAt":1790000300000}},
 "notes":[{"id":"k3j2a9x1lq","name":"Token refresh","state":"failed","error":"HTTP 503"}]}
```
1. Istotne są definicje z `envId == envId` i włączoną regułą dla jednej z `names`.
2. Dla harmonogramów włączonych i niezatrzymanych:
   - przebieg w locie: `ensure` czeka na jego `done`;
   - przebieg należny: uruchamia go i czeka;
   - przebieg nienależny, ale nakładka nie ma wartości z reguły wymaganej: uruchamia go i czeka, najwyżej raz na `every` (`missFiredAt`). Reguła opcjonalna niczego nie wymusza.
3. Harmonogramu zatrzymanego `ensure` nigdy nie uruchamia. Po `stop()` odpowiada od razu bieżącą nakładką.
4. Czekanie trwa najwyżej 20 s albo do zerwania połączenia przez klienta. Zerwanie kończy czekanie, ale nie przebieg. Klient przerywa po 25 s (`AbortSignal.timeout`).
5. `notes.state`:
   - `failed`: klasa net albo retry;
   - `stopped`;
   - `config`;
   - `missing`: wyczerpany limit wymuszonych przebiegów, notatka niesie `nextAt`;
   - `running`: minął limit 20 s;
   - `expired`: harmonogram wyłączony, a `expiresAt <= now`; bez `expiresAt` warunek to `at + everySec*1000 <= now`;
   - `off`: harmonogram wyłączony, a wartości brak.

### 4.5 Ekstrakcja, klasyfikacja, limity

**Ekstrakcja (`extract.go`)**

Działa na surowych bajtach z `a.exchange`.
- **json:**
  - body musi być UTF-8 bez NUL, inaczej `Binary body`;
  - ścieżka `a.b[0].c`, opcjonalnie z `$.`; wiodący BOM jest usuwany; body do 4 MB;
  - nawigacja przez `json.RawMessage`: liczba zostaje dosłownym literałem, bool daje `true`/`false`, obiekt daje `json.Compact`, przy duplikatach wygrywa ostatni klucz;
  - `null`, brak albo `""` to błąd.
- **header:** `strings.EqualFold`, pierwsza wartość.
- **regex:**
  - RE2 przez `FindSubmatch` na pierwszym 1 MB bajtów, także spoza UTF-8;
  - grupa 1, a bez grup całe dopasowanie;
  - jeden końcowy `\r` jest obcinany;
  - wzorzec kompiluje się przy parsowaniu pliku, więc błąd od razu trafia do `problems`.
- **Wspólne zasady:**
  - wartość musi być niepusta, mieć najwyżej 64 KB i być poprawnym UTF-8, bez znaków sterujących poza TAB;
  - znak sterujący daje notatkę `Value for token contains a line break or control character`, bo `client.Do` odrzuca taki nagłówek;
  - wszystko albo nic: porażka reguły wymaganej oznacza, że nic nie jest ustawiane;
  - `expiresPath` przyjmuje liczbę albo tekst liczby w sekundach. Wartość mniejsza lub równa zero albo nieliczbowa jest ignorowana z ostrzeżeniem.

**Klasyfikacja**

| Klasa | Kiedy | Skutek |
|---|---|---|
| ok | 2xx i spełnione reguły wymagane | Nakładka ustawiona, `failures=0`. `nextAt = now + clamp(min(every, exp - lead), 10, every)`, a bez `exp` po prostu `now + every` |
| net | brak statusu (sieć, timeout, DNS) | Ponowienie po `min(30, every)` s, bez limitu prób. Przy `no such host` dla nazwy `.local` na Linuksie dochodzi `This build cannot resolve .local names (mDNS). Use the IP address.` |
| retry | 5xx, 408, 429 | Backoff 10, 30, 60, 120, 300 s, najwyżej `every`. `Retry-After` w sekundach przycięty do zakresu od 10 do 3600 s. Piąta porażka z rzędu daje `stopped` |
| stop | 3xx, pozostałe 4xx, 2xx bez wymaganej wartości, wartość ze znakiem sterującym, nie-JSON przy regule json | `stopped` od razu, po jednej próbie |
| config | brak zapytania, środowiska lub zmiennej, pole złego typu, nieczytelny plik | Nic nie wychodzi. Ponowna ocena po `every` i od razu po zmianie pliku |

Teksty przy zatrzymaniu:
- 401 i 403: `Login rejected (HTTP 401). Stopped so the account does not get locked. Fix the request, then press Run now.`
- 3xx: `Redirect (HTTP 302) instead of an answer. Stopped after one try.`
- inne 4xx: `HTTP 404. Stopped after one try.`
- pięć porażek retry: `HTTP 503 five times in a row. Stopped.`
- brak wartości: `No value for token at access_token. Stopped.`

`Run now` na harmonogramie wyłączonym zapisuje wynik w logu, ale nie ustawia `blocked`.

**Limity**

| Parametr | Wartość |
|---|---|
| włączone harmonogramy / reguły na harmonogram | 32 / 20 |
| przebiegi równoległe | 4, jeden naraz na harmonogram |
| JSON z odpowiedzi / wejście regexu / wartość | 4 MB / 1 MB / 64 KB |
| czekanie serwera w `ensure` / w `run` | 20 s / 60 s, potem 202 |
| czekanie klienta na `ensure` / na `run` | 25 s / 65 s |
| log / wycinek odpowiedzi | 20 wpisów / 2 KB, tylko po porażce |

### 4.6 UI

**Topbar**
```
[=] simple-rest [v1.2.0 linux/arm64]  [local v] [Environments] [⟳ Schedules 2] [Import] [Export] [⚙]
```
- `#schedBtn` stoi za `#envBtn` (L180) i nie ma klasy `icon`, bo `button.icon` jest przezroczysty (L30).
- Potrzebna jest własna reguła `#schedBtn .count`, bo dziś istnieje tylko `.tabs .count` (L97). Na wąskim ekranie zostaje glif z licznikiem.
- Licznik pokazuje harmonogramy włączone i niezatrzymane.
- Klasa `err`, gdy któryś ma stan `stopped` albo `config`.
- `title` ma postać np. `Schedules: 2 on, 1 stopped`.
- Gdy binarka nie odpowiada, przycisk pokazuje `?`.

**Modal `#schedModal`** (`.envlayout` jak `#envModal` L300..319, szerokość `min(860px, 96vw)`)
```
+- Schedules ----------------------------- Runs in the simple-rest program --- [x] -+
| ● Token refresh   ok 2m    | Name    [Token refresh               ]  [Delete]      |
| ! Partner login   stopped  | [x] Enabled  since 12:04, from 192.168.1.20         |
| ○ Old one         off      |                                       [Run now]     |
|                            | Request [My API / Login  POST                 v]    |
| [+ Schedule]               | Env     [local                                v]    |
|                            | Every   [ 4 ] [minutes v]                           |
|                            | Expiry  [expires_in  ] from the response, [60] s    |
|                            |         before it (leave empty to use Every only)   |
|                            | Set from the response                               |
|                            |     Variable  From      Path / header / regex   Opt |
|                            | [x] token     [JSON v]  access_token            [ ] |
|                            | [ ]           [JSON v]                              |
|                            | Last run  12:04:10  200 OK  148 ms                  |
|                            | Next run  12:07:10 (in 2:52, from expires_in)       |
|                            | token     eyJhbG... (812 chars)  [Copy]             |
|                            | Fields    access_token, expires_in, token_type      |
|                            | Recent    12:04:10  200  148 ms  token set          |
| Runs inside the simple-rest program, also with no browser open. Sends the saved   |
| request. Every start of the program turns all schedules off. Values are kept in   |
| memory only, never in the data file.                                              |
+- [Turn all off] ------------------------------------------------------ [Close] --+

| Stopped   Login rejected (HTTP 401). Stopped so the account does not get locked.  |
|           Fix the request, then press Run now.                                    |
| Response  {"error":"invalid_grant"}                                               |
| Request changed since the stop. Press Run now.                                    |
```

**Zachowanie modala**
- **Akcje:**
  - `Enabled` i `Run now` (z `AbortSignal.timeout(65000)`) nie zapisują pliku. Przed każdą akcją idzie `await flushPersist()`;
  - 422 trafia do `.hint` w modalu, a 503 daje `simple-rest is shutting down.`;
  - `Delete` przy włączonym harmonogramie najpierw woła disable, potem usuwa definicję;
  - `Turn all off` przy więcej niż jednym włączonym pyta o potwierdzenie.
- **Pola:**
  - pola zapisują się na `change`, a nie na `input`;
  - `#schedReq` grupuje zapytania w `optgroup` według kolekcji;
  - `#schedEnv` zawiera tylko konkretne środowiska.
- **Statusy na liście:** `ok 2m`, `running…`, `retry 0:30`, `stopped`, `waiting`, `off`, `no request`, `no env`, `missing {{password}}`. Odliczanie liczy się ze `skew`.
- **Wartości:**
  - `maskValue` pokazuje najwyżej `min(6, floor(len/4))` znaków plus długość, a przy wartości do 12 znaków tylko długość;
  - `Copy` kopiuje pełną wartość;
  - wygasła wartość dostaje dopisek `expired`.
- **Hinty** (`--warn`):
  - `problems`;
  - `Requests see {{token}} only when "local" is selected in the top bar.` z przyciskiem `Select it` (zmienia środowisko tylko tej karty);
  - `token is also set by schedule "Login B"`;
  - `Variable {{password}} is not set in "prod". Nothing will be sent.`;
  - RE2: `Regex uses Go syntax (RE2): no lookbehind or backreferences. Use ([^\r\n]+) to stop at the end of a line.`;
  - przy `#dirty`: `The schedule sends the saved version. Save (Ctrl+S) to use your edits.`;
  - po `Run now` na wyłączonym: `Value set once. Tick Enabled to keep it fresh.`
- **`+ Schedule`:**
  - bierze bieżące zapytanie HTTP, o ile jest zapisane; w przeciwnym razie pokazuje `Save the request to a collection first`;
  - w widoku TCP (1.3.0) pokazuje `Open an HTTP request to schedule it.`;
  - bez środowisk tworzy `Default` i aktywuje je w tej karcie;
  - nowy harmonogram jest wyłączony, z hintem `Check the rules with Run now, then tick Enabled.`

**Environments: `#envOverlay` pod tabelą**
```
Set by schedules (kept in memory until the program stops, they override the table)
token   eyJhbG... (812 chars)  "Token refresh" 12:04, expires 12:09   [Copy] [Clear]
```
- Dopiski: `overrides the table`, `(schedule deleted)` oraz `expired 12:09` w kolorze `--warn`.
- `Clear` woła `DELETE /api/vars/{envId}/{name}`.
- `kvTable` się nie zmienia.

**Sidebar** (L763..765): znacznik ⟳ przy zapytaniu z włączonym harmonogramem i przycisk `data-act="sched"` (`Schedule this request`).

**Send (`sendFlow`, wydzielone z `sendRequest` L603..627)**
1. `sending = true`.
2. `names = referencedVars(requestTexts(cur))`. Wyłączone nagłówki i nieaktywny typ auth się nie liczą.
3. Gdy definicja z `envId == state.activeEnv` ustawia jedną z `names`:
   - `await flushPersist()`;
   - `await ensureVars(names, envId)`;
   - po 300 ms status pokazuje `Refreshing token…`.
4. `eff = buildEffective(cur, effectiveVars(envId))`.
5. `transport(eff)`, wydzielone z L612..622.
6. `renderResponse(res, eff.url)`: L689 przestaje liczyć URL drugi raz.
7. `addHistory`.

Błąd `ensure` nie blokuje Send. cURL (L950) przechodzi tę samą ścieżkę.

Toast pokazuje jedną notatkę, w kolejności `stopped`, `failed`, `config`, `missing`, `running`, `expired`, `off`:
- `Schedule "Token refresh" is stopped: Login rejected (HTTP 401). Sent with the old value.`
- `Schedule "Token refresh" failed: HTTP 503. Sent with the old value.`
- `Schedule "Token refresh" could not run: Variable {{password}} is not set in "prod".`
- `{{token}} is not set yet by schedule "Token refresh". Next run at 12:07.`
- `Schedule "Token refresh" is still running. Sent with the old value.`
- `{{token}} from schedule "Token refresh" expired at 12:09 and the schedule is off. Turn it on in Schedules.`
- `{{token}} comes from schedule "Token refresh", which is off. Turn it on in Schedules.`

Błąd ręcznego Send `no such host` dla nazwy `.local`, gdy `info.os === 'linux'`, dostaje ten sam hint mDNS co harmonogram.

**Pozostałe**
- Confirmy usuwania zapytania (L770), kolekcji (L755) i środowiska (L1116) dostają dopisek `Used by schedule "X". It will stop sending until you pick another one.`
- Eksport zawiera `schedules`, bez nakładki.
- Import przemapowuje `requestId` i `envId` na nowe id (import nadaje nowe id w L900 i L903). Harmonogram bez swojego zapytania albo środowiska w pliku jest pomijany i liczony.
- Toast importu (L890) oraz tytuły `#importBtn` i `#exportBtn` (L181..182) wymieniają harmonogramy.
- `installUpdate` przy włączonych harmonogramach pyta tekstem z 3.8.

### 4.7 Zachowanie i przypadki brzegowe

**Pętla i zegar**
- Pętla: ticker co 1 s plus kanał `wake`. Harmonogram jest należny, gdy `enabled && !running && blocked == "" && now >= nextAt`.
- Co 5 s pętla wywołuje `a.currentTag()`, żeby wykryć ręczną edycję pliku.
- `now` to czas ścienny. Po uśpieniu komputera należny przebieg rusza w ciągu 1 s.
- Cofnięcie zegara (Raspberry Pi bez RTC przed NTP) wykrywa warunek `now < plannedAt - 60 s`. Wtedy `nextAt = now + (nextAt - plannedAt)`.

**Współbieżność**
- `job.gen` (disable, ponowne enable, usunięcie definicji) odrzuca status i termin przebiegu zakończonego po zmianie.
- Wartości z odpowiedzi 2xx, która już przyszła, trafiają do nakładki zawsze, więc rotowany `{{refresh}}` nie ginie.
- Enable przy przebiegu w locie nie podnosi `gen`. `Run now`, a zaraz potem `Enabled`, daje więc jedno logowanie.

**Zmiany pliku (reconcile po zmianie tagu)**

| Zmiana | Skutek |
|---|---|
| usunięta definicja | wyłączenie z wpisem `removed from the data file` i anulowanie przebiegu |
| inne `requestId`, `envId` albo zbiór wymaganych zmiennych | `nextAt = now`, `nextFrom = changed`; przy przebiegu w locie `redo`, a po nim `nextAt = min(termin, now)` |
| mniejsze `everySec` | `nextAt = min(nextAt, lastRunAt + every)` |
| harmonogram w stanie `config` | `nextAt = now` |
| usunięte środowisko | wpisy nakładki tego środowiska znikają |

**Aktualizacja**
- `a.shutdown()` przed `restartProcess` daje stan, w którym żaden przebieg nie startuje.
- Na Windows nowy proces startuje, zanim stary wyjdzie (restart_windows.go:14..17), a `restartProcess` czeka 700 ms i do 3 s na `Shutdown` (main.go:406..409). W tym oknie `run` i `enable` dostają 503, a `ensure` odpowiada od razu.

**Przypadki brzegowe**

| Sytuacja | Zachowanie |
|---|---|
| Niezapisane zmiany w edytorze | Wysyłana jest wersja zapisana; hint przy `#dirty` |
| Harmonogram używa zmiennej, którą sam ustawia (`{{refresh}}`) | Nakładka bierze udział w budowie jego zapytania |
| Zmiana Env w trakcie przebiegu | Wartość trafia do starego środowiska; przez `redo` zaraz potem rusza przebieg dla nowego |
| Dwa harmonogramy ustawiają tę samą zmienną | Wygrywa późniejszy zapis; hint o konflikcie |
| Karta używa innego środowiska niż harmonogram | `ensure` nic nie robi; hint `Select it` |
| Body nie-UTF-8 albo z NUL | json daje `Binary body`, regex działa na bajtach, header działa |
| Błąd sieci przy URL z sekretem w query | `lastError`, notatki i konsola bez URL (`url.Error.Err` przez `errors.As`) |
| Dwie binarki na jednym pliku (losowy port, main.go:441..451) | Osobne pamięci, obie wyłączone po starcie. PUT wykrywa zapis drugiego procesu przez skrót. Włączenie w obu daje podwójne logowania (ryzyko) |
| Zawieszony serwer tokenu | Send czeka najwyżej 20 s (klient 25 s), potem idzie ze starą wartością i z toastem |
| Konsola zablokowana (QuickEdit, XOFF) | Linie logu przepadają, scheduler i API działają |
| Karta 1.1.0 po aktualizacji | 409, nic nie zostaje nadpisane |

### 4.8 Bezpieczeństwo

- Cele bez filtra (odp. 5a). `verifyTls=false` działa jak przy ręcznym Send.
- Wartości:
  - nigdy nie trafiają na dysk, do eksportu, historii (zapisuje się szablon `{{token}}`) ani do konsoli;
  - wychodzą tylko w `GET /api/schedules` (po zmianie, dzięki `since`) i w `ensure`;
  - po sukcesie zapisywane są tylko nazwy pól odpowiedzi.
- Ochrona konta:
  - zatrzymanie po jednej próbie przy 3xx i 4xx innym niż 408 i 429;
  - najwyżej 5 porażek retry z rzędu;
  - jeden przebieg naraz na harmonogram;
  - wymuszony przebieg w `ensure` najwyżej raz na `every`;
  - `ensure` nie uruchamia zatrzymanych harmonogramów;
  - brak automatycznego włączenia po starcie, więc pętla restartów nie zamieni się w serię logowań.
- Przy `0.0.0.0` bez uwierzytelniania każdy w sieci widzi wartości nakładki. To ta sama klasa ekspozycji co dziś hasła w `GET /api/state` (pytanie 1).
- Przed DNS rebinding chroni reguła Host z S3, a przed clickjackingiem `Enabled`, `Run now` i `Install and restart` nagłówki z S3.
- Ruch wychodzący: program wysyła zapytania sam z siebie tylko po jawnym włączeniu harmonogramu. Zdanie README:187..188 wymaga zmiany.
- Cookie jar jest wspólny z ręcznym Send (main.go:172..176).

### 4.9 Testy

**Go (`go test ./... -count=1` oraz `-race`)**
- **S1, `main_test.go`:**
  - `TestStateVersionGuard`: 409 tekstem, plik bez zmian;
  - `TestStateTag`:
    - ETag to skrót podawanych bajtów, także dla `null`;
    - pasujące `If-None-Match` daje 304;
    - zewnętrzny `os.WriteFile` zmienia tag;
    - dwa `newApp` na jednym pliku mają ten sam tag, więc restart nie zmienia tagu;
  - `TestStateIfMatch`:
    - brak nagłówka daje 428;
    - niezgodny tag daje 412 z dokumentem i `ETag`, a plik się nie zmienia;
    - dwa PUT z tym samym `If-Match`: drugi dostaje 412;
  - `TestStateRoundTrip` (main_test.go:376) dostaje nagłówki.
- **S4, `effective_test.go`:**
  - `TestEffectiveVectors`;
  - `TestJSSpaceClass`: klasa `[\s\x0B\x{A0}\x{FEFF}\x{2028}\x{2029}\p{Zs}]` daje 25 punktów kodowych;
  - `TestJSTrim`, `TestEncodeURIComponent`, `TestSubstSinglePass`;
  - `TestDecodeRequestTypes`: liczba w polu tekstowym daje problem ze ścieżką, brak `options` daje settings, timeout 1.5 daje 1.
- **S5, `extract_test.go`:** `TestJSONPick`, `TestExtractRules` (regex na bajtach z `\xbf` i NUL, obcinanie `\r`, znaki sterujące, wszystko albo nic), `TestClassify`, `TestNextDelay`, `TestRetryDelay`.
- **S5, `schedule_test.go`** (wstrzyknięte `now`, ręczne `tick()`, cel na `httptest` z licznikiem `atomic.Int32` jak `fakeHub` w update_test.go:201..240):
  - `TestSchedulerStartsOff`: ręczne `"enabled": true` w pliku daje 0 wywołań po 10 tickach;
  - `TestEnableRunsAtOnce`, `TestStopOn401` (dokładnie 1 wywołanie), `TestRetry503`;
  - `TestNetError`: URL z `?key=SECRET` nie pojawia się w snapshocie, w `ensure` ani w logu; `.local` daje hint na linux;
  - `TestConfigMissingVar`, `TestRunNow`, `TestGenDiscard`, `TestRedoAfterChange`, `TestConcurrencyLimit`;
  - `TestEnsure`: czekanie, wymuszenie, `missing`, `expired`, `off`, zerwanie przez klienta;
  - `TestReconcile`, `TestSnapshotAtomic`, `TestClockJump`;
  - `TestViewCache`: bez zmiany tagu plik nie jest czytany;
  - `TestExternalEditDetected`;
  - `TestSchedulerNeverWritesDataFile`;
  - `TestShutdownReleasesWaiters`;
  - `TestShutdownRace` (z `-race`): 50 równoległych `run` i `ensure`;
  - `TestBlockedLog`: writer blokujący się na zawsze;
  - `TestSchedulesAPI`: 200 i 204 z `since`, 404, 409, 413, 415, 422, 503, 405 z `Allow`, 400 dla złego id, obcy Origin 403.
- **S3, `main_test.go`:**
  - `TestHostAllowlist`: przechodzą IP, `[::1]`, `localhost`, `raspberrypi`, `pi.local`, `pi.lan`, `pi.home.arpa`, `pi.internal`, `pi.localdomain` i `os.Hostname()`; `evil.example.com` dostaje 403; `-allow-host` i `*` przepuszczają; `loopbackOnly` działa jak dziś;
  - `GET /` ma nagłówki przeciw ramkom;
  - `TestLogQueueDrops`.

**Node (`node --test "tests/**/*.test.mjs"`)**
- **S1, `tests/state.test.mjs`:**
  - `normalizeDoc`: klucz `future` przeżywa na każdym poziomie, tabela typów, `{{constructor}}`, `__proto__`;
  - `mergeState`:
    - definicja z A i historia z B przetrwają obie;
    - usunięcie bez zmiany po drugiej stronie wygrywa;
    - zmiana kontra usunięcie zostawia zmianę;
    - ta sama jednostka po obu stronach: wygrywa „mine” i jest konflikt;
    - ustawienia po kluczu, `activeEnv` z karty;
  - `persistNow` z podmienionym `fetch`:
    - jeden PUT w locie i nagłówki;
    - 409 ustawia `stale`;
    - 412 scala i ponawia, najwyżej 3 razy;
  - `syncIfBehind`:
    - 304;
    - zaległy zapis idzie przez `flushPersist`;
    - fokus w polu z niezatwierdzoną wartością odkłada synchronizację do `focusout`;
    - czysty edytor wczytuje zmienione zapytanie;
  - `activeEnv` z `sessionStorage`.
- **S2, `tests/ui.test.mjs`:** auth z folderu i kolekcji, `noauth`, zmienne kolekcji, liczniki i `skipped` z `importData`.
- **S4, `tests/effective.test.mjs`:** te same wektory; lista białych znaków równa `/\s/` liczonemu na silniku; `encodeURIComponent`.
- **S6, `tests/schedules.test.mjs`:**
  - `normalizeSchedule`, `requestTexts`, `referencedVars`, `schedulesSetting`;
  - `effectiveVars`: nakładka wygrywa, środowiska się nie mieszają;
  - `maskValue`, `isExpired`, `varConflicts`, `schedulesUsing`;
  - eksport i import z mapowaniem id (bez zapytania albo środowiska: pominięty);
  - `applySnapshot`: inny `boot` daje toast, inna wersja daje `stale`, `stateTag` woła `syncIfBehind`;
  - `sendFlow`: kolejność `flushPersist`, `ensure`, `buildEffective`; błąd i timeout `ensure` nie blokują; kolejność toastów; tryb bez binarki;
  - `featureState`: `nobin` i `stale` blokują akcje.

**e2e (S5, `scripts/e2e-update.sh`)**
1. Przed startem kopii 0.9.0 skrypt zapisuje:
   - `"$root/hub/token.json"` z `{"access_token":"e2e","expires_in":300}`;
   - `"$root/app/data.json"` z kolekcją (`GET http://127.0.0.1:8791/token.json`), środowiskiem i harmonogramem;
   - `sha256sum` pliku danych.
2. `curl -s -X POST -H 'Content-Type: application/json' -d '{}' "$api/schedules/s1/enable"`, potem czekanie na `"value":"e2e"`.
3. Po instalacji i restarcie sprawdza:
   - brak `"enabled":true` i brak `"value":"e2e"`;
   - inny `boot`;
   - niezmieniony `sha256sum`;
   - liczba `GET /token.json` w `hub.log` nie rośnie przez 3 s.

**Ręcznie (S6)** z serwerem tokenu w `node:http` (licznik wywołań, przełączniki 200, 401, 503, `Retry-After`, zawieszenie, CRLF, cp1250):
- 401 zwiększa licznik o 1;
- 15 min bez otwartej karty;
- drugi komputer albo telefon: włączenie, `from 192.168.x.y`, zamknięcie karty, świeży token na A;
- edycja definicji z B widoczna na A, także z kursorem w modalu;
- równoległa edycja zapytania i definicji na A i B: obie przetrwały;
- zmiana środowiska na B nie zmienia środowiska na A;
- `Run now`, a zaraz potem `Enabled`: licznik +1;
- QuickEdit na Windows 10;
- uśpienie laptopa;
- Raspberry Pi bez pulpitu po IP i po `raspberrypi.local`;
- aktualizacja w trakcie przebiegu;
- strona w `<iframe>` na obcym originie się nie ładuje, a podgląd HTML (L714) działa.

## 5. Funkcja 2: konsola TCP (1.3.0)

### 5.1 Cel i zakres

**Cel.** Konsola:
- utrzymuje połączenie TCP z urządzeniem lub systemem w sieci;
- wysyła komunikaty z `{{zmiennymi}}`;
- dzieli odbiór na komunikaty według presetu;
- mierzy czas odpowiedzi.

Działa w każdej przeglądarce, która ma dostęp do UI binarki.

**MVP:**
- klient w binarce, sesje kluczowane id definicji, asynchroniczny Connect z anulowaniem;
- keepalive (Idle 15 s, Interval 5 s, Count 3), a na Linuksie `TCP_USER_TIMEOUT` 30 s;
- ramkowanie i opakowanie w Go, Wait for reply nieblokujący;
- zapis z postępem;
- strumień zdarzeń (3.9);
- zakładka `TCP`, widok `#tcpView`, log Text/Hex, edytor Text/Hex z escape'ami;
- zapisane komunikaty, eksport i import.

**Poza zakresem:** nasłuch (odp. 1).

**Później:**
- ramkowanie Custom i zmiana ramkowania bez rozłączania;
- heartbeat w `tcpHub`, auto-reconnect, automatyczny ACK;
- korelacja odpowiedzi po identyfikatorze (MSH-10 ↔ MSA-2);
- ASTM z sumą kontrolną;
- zmienne z RX, `{{$now}}`, `{{$counter}}`;
- TLS, UDP, wysyłka w windows-1250, zapis logu do pliku.

### 5.2 Architektura

```
przeglądarka (dowolny komputer w sieci)                  binarka                                    urządzenie
 state.tcp (definicje) --PUT /api/state If-Match--> plik danych (view z cache po tagu)
 tcpEncode(szablon) = parts --POST /api/tcp/{id}/send {parts,src,...}--> tcpHub: effectiveVars(envId sesji),
                                                                          wrap, zapis z postępem --------> host:port
 POST /api/tcp/{id}/connect {envId,host,port,...} --> podstawienie hosta i portu, net.Dialer (keepalive)
 EventSource /api/events  <--- tcp.*, state.changed, sched.changed, ready --- eventBus (<boot>.<seq>)
```

**Podział odpowiedzialności**
- **UI:**
  - definicje;
  - escape'y i tryb Hex, czyli zamiana szablonu na `parts`;
  - podgląd `Sends:`;
  - `ensureVars` przed Connect i Send.
- **Go:**
  - podstawienie zmiennych (plik plus nakładka, 3.5);
  - gniazdo, ramkowanie w obie strony, Wait for reply;
  - log w pamięci, zdarzenia.

**`tcp.go`**
- Struktury:
  - `tcpHub{mu, sessions, maxActive 16, maxRecords 32}`;
  - `tcpSession{mu, wmu, info, log, cur *tcpConn}`;
  - `tcpConn{gen, conn, cancel, deframer, waiter}`, nowy przy każdym Connect.
- Dial, `readLoop` i timer waitera sprawdzają pod `s.mu`, czy `s.cur == my`. Jeśli nie, kończą się po cichu. Dzięki temu szybki Reconnect nie psuje nowego połączenia.
- Kolejność blokad: `hub.mu → s.mu → bus.mu`.
- **Connect:**
  - od razu odpowiada 202 `connecting`;
  - dial działa w goroutine z `context.WithTimeout`;
  - host i port to szablony podstawiane w `effectiveVars(envId)`;
  - zostawione `{{x}}` daje 400 `Variable {{lab_host}} is not set in "local".`
- **Send:**
  1. Pod `wmu` i `s.mu`:
     - sprawdza stan (`connected`, brak czekania, inaczej 409);
     - składa bajty z `parts`: `{b64}` to bajty dosłowne, `{v}` to wartość zmiennej jako UTF-8, bez interpretacji escape'ów; brak wartości daje dosłowne `{{x}}` i nazwę w `missing`;
     - opakowuje bajty;
     - wypycha bajty spoza ramki (`flushOutside`);
     - publikuje wpis TX z seq;
     - przy `waitReply` rejestruje waiter.
  2. Po zwolnieniu `s.mu` idzie zapis z terminem 10 s odnawianym po każdym postępie. Timeout przy `n == 0` daje `write timeout: no progress for 10 s`.
  3. Odpowiedź HTTP przychodzi zaraz po zapisie.
- **Wait for reply:**
  - odpowiedzią jest pierwszy wpis RX, którego pierwszy bajt przyszedł nie wcześniej niż `t0`;
  - wpis dostaje `replyTo` i `rttMs`, liczone na zegarze monotonicznym;
  - inne wyniki to timeout (`no reply within 5 s`), `DELETE .../wait` i zamknięcie w trakcie.

**`tcpframe.go`** (czysty kod, bez `net`): `framingSpec`, `wrap`, `deframer` z metodami `feed`, `idle`, `flushOutside`, `flush` i `pending`.

**`events.go`**
- Dziennik 4096 zdarzeń albo 8 MB, z narastającą sumą rozmiarów.
- Kursor ma postać `<a.boot>.<seq>`.
- Subskrypcja:
  1. rejestracja;
  2. replay albo migawka od providerów `tcp`, `state` (`{tag}`) i `sched` (`{gen}`);
  3. `ready`;
  4. bieżące zdarzenia.
- Migawka zamiast replayu idzie, gdy:
  - nie ma kursora albo ma zły format;
  - `boot` jest inny;
  - `seq > current`;
  - w dzienniku jest luka;
  - zaległość przekracza 2 MB.
- Limity:
  - 32 strumienie;
  - kolejka 4 MB;
  - `: ping` co 20 s;
  - `retry: 2000`;
  - `SetWriteDeadline` 10 s przez `http.NewResponseController`.
- `SetEscapeHTML(false)`, więc `&`, `<` i `>` z HL7 nie rosną do `\u0026`.

**`tcp_linux.go` i `tcp_other.go`:** `TCP_USER_TIMEOUT` = 30000 ms przez literał `0x12`. W Go 1.26 `syscall` ma tę stałą tylko w `zerrors_linux_arm64.go:1331`, a dla amd64 jej nie ma (sprawdzone). Na innych systemach no-op.

### 5.3 Model danych

**Plik (`tcp`, `version` zostaje 1)**
```json
"tcp": [{
  "id": "m1x2k9qalw3z0f", "name": "Lab analyzer", "host": "{{lab_host}}", "port": "2575",
  "connectTimeoutMs": 10000, "framing": { "preset": "none" }, "idleMs": 50,
  "wrap": true, "waitReply": false, "replyTimeoutMs": 5000,
  "mode": "text", "newline": "cr", "escapes": true, "rxCharset": "utf-8",
  "messages": [{ "id": "q7c1a0b2mf0x3", "name": "ORM^O01", "mode": "text", "data": "MSH|^~\\&|...\rPID|1||{{patient_id}}" }]
}]
```

| Pole | Wartości, domyślna |
|---|---|
| `host` / `port` | szablony z `{{zmiennymi}}`. Po podstawieniu: nazwa, IPv4 albo IPv6 (z `[]` lub bez, ze strefą) / liczba od 1 do 65535. Wklejone `tcp://h:p` albo `h:p` UI rozdziela na dwa pola |
| `connectTimeoutMs` / `replyTimeoutMs` | od 100 do 60000, domyślnie 10000 / 5000 |
| `framing.preset` | `none` (domyślny), `lf`, `crlf`, `mllp`, `stxetx`. Obiekt zostawia miejsce na Custom |
| `idleMs` | Split on silence, od 0 do 5000, domyślnie 50. Działa tylko dla bajtów spoza ramki; 0 oznacza jeden wpis na jeden `Read` |
| `wrap`, `waitReply` | domyślnie `true` / `false` |
| `mode`, `newline`, `escapes`, `rxCharset` | `text`/`hex`; `lf` (domyślny), `cr`, `crlf`; tylko `\r \n \t \\ \xHH`; `utf-8`, `windows-1250`, `iso-8859-2`, `latin1` (tylko wyświetlanie RX) |

- Szkic edytora leży w `localStorage['simple-rest-tcp-draft:<id>']` tej przeglądarki, a nie w pliku.
- Pola definicji zapisują się na `change` i `blur`.
- `normalizeTcp`:
  - przycina liczby do zakresów;
  - nieznany preset zamienia na `none`;
  - zachowuje nieznane pola.

**Pamięć binarki**
- `tcpInfo {id, rev, state, envId, envName, hostTpl, portTpl, host, port, local, remote, connectedForMs, bytesIn, bytesOut, error, framing, idleMs, pending, waiting, openedBy}`:
  - `rev` to seq ostatniej zmiany; UI przyjmuje tylko większy;
  - `connectedForMs` pochodzi z zegara monotonicznego.
- `tcpEntry {seq, conn, at, dir: tx|rx|info|error, size, data(base64), src?, by?, wrapped?, outside?, incomplete?, cut?, dropped?, replyTo?, rttMs?, timedOut?, closed?, missing?}`.
- Limity:
  - 16 sesji aktywnych, 32 rekordy;
  - log 1000 wpisów albo 2 MB na sesję;
  - ramka 1 MB, bajty spoza ramki 64 KB na wpis;
  - wysyłka 1 MB po podstawieniu;
  - `src` 64 KB, ciało żądania 2 MB.
- Log nie trafia do pliku, historii ani eksportu.

**Zgodność**
- Plik bez `tcp` daje `[]`.
- Cofnięcie do 1.2.0 zachowa `tcp` (F1), a do 1.1.0 je zgubi.
- Eksport z `tcp` zaimportowany w 1.2.0 daje `Skipped: tcp (needs a newer simple-rest)` (F8).

### 5.4 Ramkowanie

| Preset | Początek | Koniec |
|---|---|---|
| None | brak | brak |
| LF | brak | `0A` |
| CRLF | brak | `0D 0A` |
| MLLP | `0B` | `1C 0D` |
| STX/ETX | `02` | `03` |

- Ramka obejmuje znaczniki, więc log i Hex pokazują prawdę o kablu.
- **Bajty spoza ramki** (przed znacznikiem początku; przy None wszystkie bajty) nie są odrzucane. Wychodzą jako wpis RX `outside`, gdy:
  - przychodzi znacznik początku;
  - minie `idleMs` ciszy;
  - uzbiera się 64 KB;
  - idzie wysyłka;
  - połączenie się zamyka.

  Dzięki temu ACK, NAK czy ENQ są widoczne.
- **Dzielenie po ciszy** działa przez `SetReadDeadline(lastByte + idle)`. Timeout odczytu nie zamyka gniazda.
- **Niedomknięta ramka** nigdy nie jest dzielona po ciszy. Po 1 s ciszy UI pokazuje `Waiting for the end of a frame: 12 B`. Przy zamknięciu część wychodzi jako `incomplete`.
- **Znacznik początku wewnątrz ramki** zamyka dotychczasową część jako `incomplete` i zaczyna nową ramkę.
- **Ramka ponad 1 MB:**
  - od razu wychodzi wpis `cut` z pierwszym 1 MB;
  - reszta do znacznika końca jest odrzucana;
  - na końcu pojawia się `dropped N B of an oversized frame`.
- **Opakowanie przy wysyłce:** `start + data + end`. Komunikat, który już zaczyna się od `start` i kończy `end`, idzie bez zmian (`wrapped: false`). Przy LF i CRLF dokładany jest tylko brakujący koniec.
- Wyszukiwanie znacznika zaczyna się od `len(buf) - len(delim) + 1`, więc nie ma kosztu O(n²).

### 5.5 API

Trasy przechodzą przez `guard` i konwencje z 3.7. Po `a.shutdown()` dostają 503.

| Metoda i ścieżka | Ciało | Odpowiedź | Błędy |
|---|---|---|---|
| `GET /api/events[?after=<boot>.<seq>]` | | `text/event-stream` (3.9) | 403, 405, 503 `too many event streams` |
| `GET /api/tcp` | | 200 `{boot, seq, client, sessions}` | 405 |
| `GET /api/tcp/{id}/log[?before=<seq>&limit=500]` | | 200 `{boot, seq, dropped, entries}` | 404 |
| `POST /api/tcp/{id}/connect` | `{"envId","host","port","timeoutMs","framing":{"preset"},"idleMs"}` | 202 `tcpInfo` (`connecting`), wynik przez `tcp.state` | 400 (zmienna, host, port, preset), 409 `already connected` / `too many sessions (16)`, 413 |
| `POST /api/tcp/{id}/send` | `{"parts":[{"b64":"..."},{"v":"patient_id"}],"src":"<szablon>","wrap":true,"waitReply":true,"replyTimeoutMs":5000}` | 200 po zapisie `{seq, size, wrapped, waiting, missing}` albo `{seq, error}` | 400, 404, 409 `not connected` / `waiting for the reply to #N`, 413 |
| `DELETE /api/tcp/{id}/wait` | | 204, idempotentne | 404 |
| `POST /api/tcp/{id}/disconnect` | `{}` | 200 `tcpInfo`, idempotentne; przy `connecting` anuluje dial | 404 |
| `DELETE /api/tcp/{id}` / `DELETE /api/tcp/{id}/log` | | 204, zdarzenie `tcp.removed` / `tcp.cleared` | 404 |

Każde zdarzenie poza `ready` ma `id: <boot>.<seq>`. `Last-Event-ID` ma pierwszeństwo przed `?after`.

### 5.6 UI

**Widok i nawigacja (`setView('http'|'tcp')`, niezależne od `setSideTab`)**
- Trzecia zakładka sidebaru `TCP` z licznikiem połączonych sesji. `setSideTab` (L800..805) przechodzi na mapę zakładek, a `#sideSearch` (L1094) filtruje też listę TCP.
- Wybór definicji TCP przełącza na `tcp`. `openRequest` (L795..799), kliknięcie w historii i `New` przełączają na `http`.
- Widok i wybrana definicja leżą w `sessionStorage['simple-rest-view']`, więc przeżywają przeładowanie po aktualizacji.
- `section#tcpView` w `main.work` przełącza się klasą `.work.view-tcp`. Nie używa atrybutu `hidden`, bo `.request{display:flex}` (L85) z nim wygrywa. Nie używa też `.tabs.inner` ani `.panel`, bo łapie je handler w L1081..1084.
- Skróty idą przez `keyAction(e, view)` z globalnego handlera (L1075..1079). W widoku TCP:
  - Ctrl+Enter woła `tcpSend`;
  - Ctrl+S otwiera `Save as…` komunikatu;
  - HTTP-owe `save()` (L808..839) nie jest wołane.
- `syncFromFile` przerysowuje `#tcpView` po id definicji. Definicja usunięta gdzie indziej przy żywej sesji pokazuje `(not saved)` i przycisk Disconnect.
- `#menuBtn` (L174) zmienia title na `Menu`.

```
+------------------------------------------------------------------------------------------+
| [Lab analyzer                                                  ]  [Duplicate] [Delete]   |
| Host [{{lab_host}}        ] Port [2575 ]  [Disconnect]   ● Connected 00:04:12      Live  |
|   10.0.0.2:51234 -> 10.0.0.5:2575 · env local · TX 312 B · RX 98 B · opened by 192.168.1.30 |
| Framing [MLLP (HL7)     v]  0B ... 1C 0D   [x] Wrap outgoing   Split on silence [50] ms  |
| This tab uses "prod". This connection was opened with "local" and sends with its values. |
+------------------------------------------------------------------------------------------+
| Log  14 entries                     RX charset [UTF-8 v]  [Text|Hex]  [Wrap]  [Clear]    |
| 12:01:03.120  ··  connected to 10.0.0.5:2575 from 10.0.0.2:51234                         |
| 12:01:05.442  TX  312 B  <VT>MSH|^~\&|SIMPLE|LAB|||||ORM^O01|42|P|2.5<CR>                |
|                          PID|1||12345<CR><FS><CR>      [Copy] [Copy escaped] [To editor] |
| 12:01:05.501  RX   98 B  <VT>MSH|^~\&|LAB|...|ACK^O01|...<CR>MSA|AA|42<CR><FS><CR>       |
|                          reply to #41 in 59 ms                                           |
| 12:01:09.003  RX    1 B  <ACK>                                          outside frame    |
| Waiting for the end of a frame: 12 B                                                     |
| Saved: [ORM^O01 ▶] [ADT^A01 ▶] [ENQ ▶]                                    [Save as…]     |
| (•) Text ( ) Hex    Enter inserts [CR v]    [x] Escapes (\r \n \t \\ \xHH)               |
| | MSH|^~\&|SIMPLE|LAB|||||ORM^O01|42|P|2.5                                             | |
| | PID|1||{{patient_id}}                                                                | |
| Sends: <VT>MSH|^~\&|...<CR>PID|1||12345<FS><CR>   (wrapped: MLLP)                        |
| 318 bytes   [x] Wait for reply [5] s      Reply in 59 ms            [Send]  Ctrl+Enter   |
+------------------------------------------------------------------------------------------+
```

**Zachowanie**
- **Framing:**
  - lista: `None (raw bytes)`, `LF`, `CR LF`, `MLLP (HL7)`, `STX/ETX`, obok bajty presetu;
  - `Split on silence` jest widoczne tylko przy None, MLLP i STX/ETX;
  - zmiana przy połączeniu pokazuje `Framing changes apply after Reconnect.` z przyciskiem `Reconnect`.
- **`Sends:`:**
  - pokazuje bajty po podstawieniu w środowisku sesji, z kopią nakładki `ovl`, i z ramką z `info.framing`;
  - komunikat już opakowany pokazuje `already framed, sent as is`.
- **Pasek edytora:**
  - liczba bajtów;
  - `unknown variable: {{x}}` (wysyłka jest dozwolona, jak w HTTP);
  - błąd hex;
  - JSON z `\` przy włączonych escape'ach;
  - `Live updates are off: values may be stale.`;
  - hint z notatki `ensure`, np. `{{token}} comes from schedule "Token refresh", which is off.`
- **Connect:**
  - przed nim idą `flushPersist()` i `ensureVars(names, envId karty)`;
  - jest zablokowany przy zostawionym `{{x}}` albo porcie spoza zakresu;
  - `no such host` dla nazwy `.local` na Linuksie dostaje hint mDNS.
- **Send:** przed nim idą `flushPersist()` i `ensureVars(names, info.envId)`.
- **Wait for reply:**
  - przycisk `Waiting for reply… [Cancel]`;
  - wynik: `Reply in 59 ms`, `No reply within 5 s` albo `Connection closed while waiting`;
  - długi zapis pokazuje `Sending…` z `bytesOut`.
- **Log:**
  - w DOM jest tylko otwarta sesja, najwyżej 1000 wpisów; wpis ponad 4 KB jest skracany w widoku;
  - etykiety: `outside frame`, `incomplete (peer closed)`, `incomplete (disconnected)`, `cut at 1 MB`, `dropped N B`, `reply to #N in X ms`, `by 192.168.1.31`;
  - wszystko renderowane przez `textContent` albo `esc` (L356).
- **To editor:** bierze `src` (szablon), a bez `src` bajty jako escape'y albo hex. Przy `wrapped` zdejmuje ramkę.
- **Stan zmian:** `Connected to 10.0.0.5:2575. Reconnect to apply changes.` pojawia się, gdy szablon hosta lub portu, wartość po podstawieniu w środowisku sesji, preset albo `idleMs` różnią się od `info`.
- **Tryb bez binarki i `stale`:** wzorzec z 3.10.
- **Mobile (do 800 px):** jedna kolumna, wiersz Framing się zawija, sidebar zostaje szufladą.

### 5.7 Zachowanie i przypadki brzegowe

**Połączenie**
- Drugi Connect daje 409 i toast `Already connected (another tab or computer?)`.
- Disconnect w trakcie dialu dopisuje `connect cancelled`.
- Odmowa, timeout, DNS i `no route to host` dają stan `closed` z `error`.
- EOF wypycha bufor (`incomplete`) i dopisuje `closed by peer`. RST daje wpis `error`.
- Martwy peer:
  - bez ruchu wykrywa go keepalive po około 30 s;
  - z danymi w locie na Linuksie wykrywa go `TCP_USER_TIMEOUT` po 30 s;
  - na Windows czas trzeba zmierzyć.

**Wysyłka**
- Kolejność wysyłek z wielu kart i komputerów szereguje `wmu`. Kolejność na kablu równa się kolejności seq TX.
- W trakcie czekania każda wysyłka do tej sesji dostaje 409.
- Czekanie nie trzyma żadnego połączenia HTTP.

**Wielu klientów i środowisko sesji**
- Sesje są wspólne dla wszystkich przeglądarek. `openedBy`, `by` i `client` pozwalają odróżnić „you” od innych adresów.
- Sesja ma środowisko z chwili Connect (`envId` karty, która połączyła). Send podstawia w środowisku sesji, niezależnie od środowiska karty, więc bajty na kablu nie zależą od tego, kto wysyła.
- Karta z innym środowiskiem widzi hint ze szkicu. Zmiana środowiska sesji wymaga Disconnect i Connect.
- Urządzenie przyjmujące jednego klienta zostaje zajęte, dopóki ktoś nie da Disconnect, także po zamknięciu kart.

**Start, restart i zamknięcie**
- Po każdym starcie nie ma żadnej sesji i nic nie łączy się samo.
- Aktualizacja:
  - `a.shutdown()` wysyła FIN, czeka najwyżej 1 s i kończy strumienie;
  - EventSource po 503 ponawia otwarcie i dostaje `tcp.snapshot` z pustą listą.
- Unix: `syscall.Exec` z close-on-exec zamyka gniazda także bez `a.shutdown()`.
- Windows: klient TCP nie zajmuje portu nasłuchu, więc nowy proces nie ma konfliktu.
- Zegar: czasy `at` pochodzą z komputera z binarką. `rttMs` i `connectedForMs` nie zależą od korekt zegara.

### 5.8 Bezpieczeństwo

- `/api/tcp*` i `/api/events` przechodzą przez ten sam `guard` co reszta, z regułą Host i nagłówkami z S3. Nie ma osobnych reguł dla TCP.
- EventSource z obcej strony wysyła Origin i dostaje 403. EventSource z tej samej strony Origin nie wysyła i przechodzi. Żaden GET niczego nie zmienia.

| Zagrożenie | Dziś (HTTP) | Z konsolą TCP | Ochrona |
|---|---|---|---|
| Ktoś w sieci steruje binarką przy `0.0.0.0` | proxy HTTP do dowolnego hosta, także loopbacku tej maszyny | dowolne bajty do dowolnego `host:port` | domyślnie 127.0.0.1, README „tylko w zaufanej sieci”, pytanie 1 |
| DNS rebinding przy `0.0.0.0` | możliwy | możliwy i groźniejszy | S3: reguła Host |
| CSRF z innej strony w tej przeglądarce | blokuje kontrola Origin | tak samo, także EventSource | testy `TestGuard` |
| Clickjacking | Send, Install update | ▶ wysyła komunikat | S3: `X-Frame-Options`, `frame-ancestors 'none'` |
| Odczyt danych z sieci | `/api/state` | log TCP (np. dane pacjentów) | tylko pamięć; README |
| Skanowanie sieci przez binarkę | możliwe | łatwiejsze | nieusuwalne w kliencie; README |
| Wyczerpanie zasobów | brak limitów | limity z 5.3 i 5.2 | limity |
| XSS z RX | | bajty z urządzenia w DOM | `textContent`/`esc`, test z `<script>` |

### 5.9 Testy

**Go**
- **`tcpframe_test.go`:**
  - tabela dla każdego presetu, karmiona całością, bajt po bajcie i w losowych kawałkach z ziarnem;
  - MLLP: dwie ramki w jednym `Read`, `1C` i `0D` w osobnych odczytach, `0B` w ramce, śmieci przed `0B`;
  - przepełnienie: `cut`, potem `dropped`;
  - `flush`, `idle`, `flushOutside`;
  - `framingSpec`, `wrap`;
  - `FuzzDeframer`;
  - wektory z `testdata/tcp-framing.json`.
- **`tcp_test.go`** (serwery na `127.0.0.1:0`, wzór z main_test.go:240..252):
  - echo od 0x00 do 0xFF;
  - TX przed RX;
  - 20 goroutines: kolejność na kablu równa kolejności seq;
  - MLLP z ACK po 50 ms: `waiting`, `replyTo`, `rttMs` co najmniej 50;
  - brak odpowiedzi, 409 w trakcie czekania, `DELETE .../wait`, zamknięcie w trakcie czekania;
  - `X` przed wysyłką nie jest odpowiedzią;
  - None z `idleMs`;
  - peer zamyka w połowie ramki;
  - zapis z postępem: wolny czytelnik dostaje całość, brak czytania daje `write timeout`;
  - `[::1]` (pominięty, gdy brak IPv6);
  - odmowa, dial do `192.0.2.1` z timeoutem 200 ms, anulowanie dialu;
  - 200 razy Reconnect pod obciążeniem;
  - `rev`, limity, walidacja (400, 404, 405 z `Allow`, 409, 413);
  - `TestTCPSubstitution`: wartość obecna tylko w nakładce trafia na kabel; brak wartości daje dosłowne `{{x}}` i `missing`; Connect z `{{lab_host}}`; sesja używa swojego `envId`;
  - `a.shutdown()` z `-race`, 100 sesji, potem 503.
- **`events_test.go`:**
  - migawka, wznowienie, inny `boot`, luka, `seq > current`;
  - `ready`;
  - zaległość 6 MB daje migawkę;
  - przepełniona kolejka zamyka strumień;
  - 33. strumień dostaje 503;
  - `&` zostaje `&`;
  - `state.changed` po PUT i po zewnętrznej edycji;
  - `sched.changed` łączone;
  - `Shutdown` ze strumieniem kończy się w czasie poniżej 500 ms.
- **`tcp_linux_test.go`:** `getsockopt(TCP_USER_TIMEOUT)` równa się 30000.
- **`main_test.go`:** `TestGuard` (319..374) obejmuje `/api/tcp` i `/api/events`: obcy Origin 403, Host spoza listy 403.

**Node (`tests/tcp.test.mjs`)**
- `tcpEncode` → `parts`:
  - escape'y, `\xHH` jako jeden bajt;
  - `{{x}}` jako `{v}` w trybie Text i Hex;
  - Enter według `newline`;
  - błędny hex;
  - 1 MB mieści się w 2 MB ciała, a `src` ponad 64 KB jest pomijany.
- `parseHex`, round trip `escapeBytes`, `tcpVisible` (`<script>`, windows-1250, BOM), `hexDump`, `bytesToB64` dla 1 MB.
- `tcpTarget`, `normalizeTcp`, `tcpWrap` na wspólnych wektorach.
- `tcpReduce`: deduplikacja, obcy `boot`, `rev`, `clearedAt`, `replyTo`.
- `previewBytes`: wartość z `ovl` widoczna w `Sends:`; środowisko sesji kontra środowisko karty.
- `setView`, `keyAction` (Ctrl+S w TCP nigdy nie woła `save`).
- `mergeState` z `tcp` po id; eksport i import z `tcp`; `featureState`.

**e2e (T1)**
- Pythonowy serwer TCP na porcie 8793 odsyła ACK MLLP i zapisuje `closed` przy FIN.
- Przed aktualizacją: `connect` z `mllp`, `send` z `waitReply`, wpis RX z `replyTo`.
- Po restarcie:
  - `GET /api/tcp` zwraca `sessions: []`;
  - serwer zapisał `closed`;
  - `out=$(curl -sN --max-time 2 "$api/events?after=$oldboot.5" || true)` zawiera `event: tcp.snapshot` i `event: ready`. `|| true` jest potrzebne, bo kod 28 przy `set -eu` (scripts/e2e-update.sh:8) przerwałby skrypt.

**Zrzuty i ręcznie (T2, T3)**
- Echo z ACK MLLP na `node:net`, port 8798. Zajęte są 8791..8793, 8795..8797 i 9333.
- Windows z `-listen 0.0.0.0` i drugi komputer: ten sam log, czekanie i etykiety `by`; definicja z A widoczna na B bez przeładowania.
- Symulator w presetach None i właściwym.
- Raspberry Pi jako cel z `nc -l` i wyjętym kablem: `closed` w około 35 s.
- Windows z danymi w locie: pomiar czasu wykrycia martwego peera.
- `.local` z binarki linuksowej: hint.

## 6. Kolejność prac i podział na PR

### 6.1 Przygotowanie (każdy krok za zgodą użytkownika)

1. **PR #6.** Według `.e2e/maps.json` jest otwarty, mergeable, z commitem `0670325`, a jego runy nie wykonały się. Stan trzeba sprawdzić. Rekomendacja: merge, bo `release.yml` startuje wtedy tylko przy zmianie `VERSION.md`. Skutek: nieudanego wydania nie ponowi kolejny push, trzeba ręcznie użyć `Run workflow` (7.4).
2. **Fast-forward `develop`.** Lokalny `develop` (`e3e2ae9`) i `origin/develop` (`d9225fc`) są przodkami `origin/master` (sprawdzone `merge-base --is-ancestor`).
   ```
   git fetch origin
   git switch develop
   git merge --ff-only origin/master
   git push origin develop
   ```
   Push do `develop` nie uruchamia żadnego workflowu.
3. Zapis tego planu w `docs/issue-7-plan.md` na `develop`. Commit robi użytkownik. `docs/**` nie uruchamia CI.
4. Komentarz w issue #7 ze streszczeniem planu i dwoma pytaniami z sekcji 9.
5. Przed pierwszym uruchomieniem e2e lokalnie trzeba przenieść robocze pliki z `.e2e/` (`maps.json`, `issue7-plan.md`, `pr*.json`), bo skrypt robi `rm -rf "$root"` (scripts/e2e-update.sh:31).
6. Przed PR `develop` → `master` sprawdzić Billing & plans.

Gałęzie funkcji wychodzą z `develop` i wracają PR do `develop`. CI tam nie startuje, więc weryfikacja jest lokalna (6.4). PR wydania `develop` → `master` uruchamia pełne CI. Gałęzie T mogą powstawać równolegle, ale merge do `develop` następuje dopiero po merge wydania 1.2.0 do `master`.

### 6.2 Zależności

| PR | Gałąź | Zależy od |
|---|---|---|
| S1 | `feature/state-sync` | nic |
| S2 | `feature/postman-import` | nic |
| S3 | `feature/host-guard` | nic |
| S4 | `feature/effective-go` | S1 (normalizacja typów i `varsOf` to kontrakt wektorów) |
| S5 | `feature/schedules-core` | S1 (tag, hook), S3 (`logq`, konwencje), S4 |
| S6 | `feature/schedules-ui` | S1, S2 (`importData`), S5 |
| S7 | `release/1.2.0` | S1..S6 |
| T1 | `feature/tcp-core` | wydane 1.2.0 (S5: `varStore`, widok pliku, `a.shutdown`) |
| T2 | `feature/tcp-ui` | T1 |
| T3 | `release/1.3.0` | T2 |

Kolejność merge: S1, S3, S2, S4, S5, S6, S7, a potem T1, T2, T3. Każdy PR zostawia `develop` gotowy do wydania. S5 dodaje API bez UI, co nic nie psuje, bo po starcie wszystko jest wyłączone.

### 6.3 PR, pliki, szacunek

**S1 `feature/state-sync` (2,5 dnia)**
- `ui.html` (+310):
  - `str`, `normalizeDoc`, `applyState`, `serializeState`;
  - `persistNow` z `If-Match`, 409, 412 i `again`, `flushPersist`;
  - `mergeState`, `reconcile`, `syncIfBehind` z odtwarzaniem fokusu;
  - `loadTabEnv`, `setActiveEnv`, `findEnv`, `varsOf`, `Object.hasOwn`, `buildEffective(r, vars)`;
  - `#staleBanner`, `persistNow` w `installUpdate`, timer 10 s w `init()`.
- `main.go` (+70): `rev`, `tag`, `stamp`, `readState`, `currentTag`, 304, 409, 428, 412, ETag, hook `onStateChange`.
- `main_test.go` (+130).
- `tests/load-ui.mjs` (15), `tests/state.test.mjs` (320), `tests/ui.test.mjs` (import z loadera).
- `README.md` (Dane), `CHANGELOG.md`.

**S2 `feature/postman-import` (0,5 dnia)**
- `ui.html` (+50):
  - auth z folderu i kolekcji: brak `auth` albo `inherit` w `fromPostmanRequest`, L942..945;
  - `noauth`;
  - `d.variable` jako środowisko `<nazwa> variables`;
  - czyste `exportData` i `importData` ze `skipped`;
  - liczba zapytań z `oauth2` w toaście.
- `tests/ui.test.mjs` (+60), `README.md` (Funkcje), `CHANGELOG.md`.

**S3 `feature/host-guard` (0,75 dnia)**
- `main.go` (+60): `hostAllowed`, `-allow-host` z `*`, linia startowa, nagłówki przeciw ramkom.
- `logq.go` (nowy, 50).
- `main_test.go` (+70).
- `ui.html` (+10, 403 w `pollUpdate`).
- `README.md` (flagi README:28..36, Bezpieczeństwo), `CHANGELOG.md`.

**S4 `feature/effective-go` (1,5 dnia)**
- `effective.go` (190): `jsSpace`, `jsTrim`, `subst`, `varsOf`, `encName`, `encValue`, `encodeURIComponent`, `buildEffective`, dekodowanie zapytania z wartościami domyślnymi i problemami typów.
- `effective_test.go` (200), `testdata/effective.json` (350), `tests/effective.test.mjs` (60).
- Komentarz w obu implementacjach: „zmiana tylko bliźniacza, z wektorem”.

**S5 `feature/schedules-core` (3 dni)**
- `extract.go` (185).
- `schedule.go` (560).
- `main.go` (+90): `boot`, `a.exchange`, trasy, `a.shutdown`, SIGTERM, czekanie `main`, `go a.sched.loop`.
- `extract_test.go` (210), `schedule_test.go` (560), `main_test.go` (+40).
- `scripts/e2e-update.sh` (+45), `CHANGELOG.md`.

**S6 `feature/schedules-ui` (2,5 dnia, z checklistą)**
- `ui.html` (+480):
  - `#schedBtn`, `#schedModal`, CSS;
  - `tick` z `?since` i `stateTag`;
  - `ensureVars`, `effectiveVars` z `ovl`, `sendFlow`, `transport`, `renderResponse(res, sentUrl)`;
  - `#envOverlay`, sidebar, confirmy, eksport i import, toasty;
  - `featureState`, hint `.local`.
- `tests/schedules.test.mjs` (260), `tests/ui.test.mjs` (lista eksportów).
- `README.md` (7.2), `CHANGELOG.md`.

**S7 `release/1.2.0` (0,5 dnia, plus ręczny krok na hubie)**
- `VERSION.md` (`1.2.0`), `CHANGELOG.md` (`## [1.2.0] - RRRR-MM-DD`).
- `scripts/screenshots.mjs`:
  - wersja fałszywego huba z `VERSION.md` zamiast `'1.1.0'` (linia 74);
  - endpoint tokenu na istniejącym `HUB_PORT` 8797;
  - enable przez `fetch` z `Content-Type: application/json`;
  - `schedules.png`.
- `site/simple-rest/index.html`, wszystkie `img/*.png` (topbar się zmienia), `site/README.md`, `README.md`.
- Opcjonalnie `runs-on: ubuntu-24.04` w `ci.yml` i `release.yml` (7.4).

**T1 `feature/tcp-core` (4,5 dnia)**
- `tcp.go` (580), `tcpframe.go` (140), `events.go` (280), `tcp_linux.go` (25), `tcp_other.go` (10).
- `main.go` (+40): pola, trasy, `a.shutdown` z `tcp.close` i `bus.close`, publikacja `state` i `sched`.
- `tcpframe_test.go` (240), `tcp_test.go` (680), `events_test.go` (320), `tcp_linux_test.go` (40), `main_test.go` (+40).
- `testdata/tcp-framing.json` (50).
- `scripts/e2e-update.sh` (+50), `CHANGELOG.md`.

**T2 `feature/tcp-ui` (4 dni)**
- `ui.html` (+850):
  - CSS 70, markup 80;
  - JS: czyste funkcje TCP, `setView`, `keyAction`, `tcpReduce`, EventSource z `live` i powrotem do odpytywania, `previewBytes`, `normalizeTcp`, scalanie `tcp` po id, eksport i import, `installUpdate`.
- `tests/tcp.test.mjs` (400), `tests/ui.test.mjs`.
- `README.md`, `CHANGELOG.md`.

**T3 `release/1.3.0` (0,5 dnia, plus ręczny krok na hubie)**
- `VERSION.md`, `CHANGELOG.md`.
- `scripts/screenshots.mjs` (echo `node:net` na 8798, `tcp.png`).
- `site/simple-rest/index.html`, obrazki, `README.md`.

### 6.4 Weryfikacja lokalna przed każdym PR

```
gofmt -l .
go vet ./...
GOOS=linux GOARCH=amd64 go vet ./...
GOOS=linux GOARCH=arm64 go vet ./...
GOOS=linux GOARCH=amd64 go test -c -o /dev/null .
go test ./... -count=1
go test -race ./... -count=1        # obowiązkowe: CI nie uruchamia -race
node --test "tests/**/*.test.mjs"
sh build.sh
sh scripts/e2e-update.sh            # najpierw przenieść pliki z .e2e/
sh scripts/release-version.sh       # przed S7 i T3: publishing=true
sh scripts/changelog-section.sh 1.2.0
```

- Sprawdzone teraz: `CGO_ENABLED=1`, gcc 16.1.0 (MinGW), więc `-race` działa lokalnie. Go 1.26.0, Node 22.18.0.
- Ścieżek tylko linuksowych (`restart_unix.go`, `tcp_linux_test.go`) nie da się uruchomić na Windows.
- Docker CLI 28.3.2 jest, ale demon Docker Desktop nie działał (`dockerDesktopLinuxEngine` nie istnieje). WSL ma tylko dystrybucję `docker-desktop`.
- Po uruchomieniu Docker Desktop:
  ```
  MSYS_NO_PATHCONV=1 docker run --rm -v "$PWD:/src" -w /src golang:1.26 sh -c 'go test -race ./... -count=1 && sh scripts/e2e-update.sh'
  ```
  Obecność `python3` w obrazie trzeba sprawdzić. W razie braku: `apt-get update && apt-get install -y python3`.
- Bez Dockera ścieżkę linuksową sprawdzi CI na PR wydania albo Raspberry Pi.

### 6.5 Szacunek

| Wydanie | PR | Dni |
|---|---|---|
| 1.2.0 | S1 2,5 · S2 0,5 · S3 0,75 · S4 1,5 · S5 3 · S6 2,5 · S7 0,5 | około 11 (od 10 do 12,5) |
| 1.3.0 | T1 4,5 · T2 4 · T3 0,5 | około 9 (od 8,5 do 10) |

Rozmiary:
- `ui.html` urośnie z 1179 do około 2020 linii po 1.2.0 i do około 2870 po 1.3.0.
- Go produkcyjny: około 1200 linii w 1.2.0 i 1100 w 1.3.0.

## 7. Dokumentacja, wersje, wydanie

### 7.1 Wersje i CHANGELOG

- Wpisy w trakcie pracy idą pod `## [Unreleased]`. Nagłówek wersji i `VERSION.md` zmieniają się dopiero w S7 i T3, bo merge z podniesionym numerem od razu publikuje, a job `site` kasuje poprzednie wydanie z Mysttic/Mysttic.
- Format pliku danych zostaje `version: 1`.

**1.2.0**
- `### Dodane`:
  - harmonogramy;
  - auth folderu i kolekcji oraz zmienne kolekcji z Postmana;
  - harmonogramy w eksporcie;
  - `-allow-host`;
  - informacja o pominiętych sekcjach przy imporcie.
- `### Zmienione`:
  - zapis pliku wymaga `X-Simple-Rest-Version` i `If-Match` (409, 428, 412), a zmiany z kilku kart i komputerów są scalane;
  - wybór środowiska dotyczy karty;
  - przy `-listen 0.0.0.0` Host musi być IP albo nazwą lokalną;
  - strony nie da się osadzić w ramce;
  - `{{constructor}}` się nie rozwija, `__proto__` działa;
  - `null` w zmiennej daje `''`;
  - ułamkowy timeout jest obcinany;
  - SIGTERM i zamknięcie okna konsoli kończą program porządnie.
- Uwaga: cofnięcie do 1.1.0 gubi `schedules` przy pierwszym zapisie.

**1.3.0**
- `### Dodane`: konsola TCP z ramkowaniem.
- `### Zmienione`: karty dostają zmiany strumieniem zamiast odpytywania.
- Uwaga: cofnięcie do 1.2.0 zachowuje `tcp`, do 1.1.0 je gubi.

### 7.2 README

Numery linii według `origin/master` `ccc0eca`. Po merge PR #6 trzeba je przeliczyć, bo PR #6 zmienia sekcję Wydania. Edytowane sekcje przepisać bez półpauz: README:112, 131, 133, 140, 155, 159.

| Sekcja | 1.2.0 (S1..S6) | 1.3.0 (T2) |
|---|---|---|
| opis README:3..5 | | REST i TCP |
| sieć README:20..22 | harmonogramy działają na maszynie z binarką; cele muszą być z niej osiągalne; wejście po IP albo nazwie lokalnej | konsola TCP z innego komputera |
| flagi README:28..36 | `-allow-host` (S3) | |
| Funkcje README:40..52 | harmonogramy, import Postmana | konsola TCP, skróty w widoku TCP |
| nowa sekcja | „Harmonogramy”: reguły, zatrzymanie po 401, wartości tylko w pamięci, restart wszystko wyłącza | „Konsola TCP”: presety, Wait for reply, środowisko sesji, wspólne sesje, log w pamięci, czasy z zegara binarki |
| Aktualizacje README:54..77 | restart wyłącza harmonogramy i czyści nakładkę; inne karty dostają toast | restart zamyka połączenia TCP |
| Dane README:79..83 | definicje w pliku, wartości tylko w pamięci, scalanie, środowisko per karta, cofnięcie do 1.1.0 | `tcp` w pliku, szkice w przeglądarce |
| Rozwój i testy README:110..141 | nowe pliki testów (README:117..118), `-race` lokalnie | `tcp.test.mjs`, testy linuksowe |
| Tryb bez binarki README:175..180 | harmonogramy widoczne, ale nie działają | konsola TCP niedostępna |
| Bezpieczeństwo README:182..188 | reguła Host, ramki, ruch wysyłany samoczynnie po włączeniu harmonogramu, wartości czytelne w sieci przy `0.0.0.0`, `.local` na Linuksie | dowolne bajty do urządzeń z sieci, skanowanie |

### 7.3 Strona, hub, zrzuty

**`site/simple-rest/index.html`**
- S7:
  - karta „Schedules”;
  - karta „No CORS in the way” (L105..110): „only talks to the program on localhost” zamienić na „talks only to the program”;
  - Privacy (L176..189): „Nothing leaves on its own” uzupełnić o harmonogramy włączone przez użytkownika;
  - alt bez numeru wersji (L164);
  - zrzut `schedules.png`.
- T3:
  - title i description (L6..7);
  - h1 i lead (L29..33);
  - karta TCP;
  - Privacy: połączenia TCP otwiera tylko użytkownik;
  - `tcp.png`.

**Zrzuty:** powstają raz na wydanie, wszystkie od nowa, bo zmienia się topbar. Poprawki w `scripts/screenshots.mjs` są w 6.3.

**Hub (ręcznie, push do innego repozytorium za zgodą użytkownika)**
- Job `site` w `release.yml` kopiuje tylko pliki wydania.
- Po udanym Release trzeba skopiować `site/simple-rest/` do Mysttic/Mysttic.
- Kartę projektu (szablon w site/README.md L13..31) trzeba zaktualizować w S7 (harmonogramy) i w T3 (TCP).
- Zdanie w site/README.md L39..41 („strona nie wymaga commitów przy kolejnych wydaniach”) trzeba poprawić: treść strony kopiuje się ręcznie przy każdej zmianie.

### 7.4 Wydanie przy zablokowanych Actions

- Według .e2e/maps.json Release po merge #5 nie wystartował z powodu billingu, a obecne workflowy nigdy nie przeszły na runnerze. Pierwszy PR wydania będzie pierwszym przebiegiem `ci.yml` z #5.
- Procedura:
  1. Billing & plans sprawdza użytkownik.
  2. PR `develop` → `master`. Run „not started” oznacza, że nie należy mergować; nic nie przepada.
  3. Zielone CI i „Release preview” z `publishing=true`.
  4. Merge.
  5. Jeśli Release nie wystartował: po odblokowaniu Actions → Release → `Run workflow` na `master` (`workflow_dispatch`). Gdy tag już powstał, a padł późniejszy job: `Re-run failed jobs`.
  6. Ręczna kopia strony na hub.
- Jeśli Actions zostaną zablokowane na dłużej, ręczne wydanie (`sh build.sh`, release i tag `simple-rest-v<wersja>` na hubie) jest możliwe tylko na decyzję użytkownika. Bez wydania na hubie updater nic nie znajdzie.
- `ubuntu-latest` przechodzi na Ubuntu 26 od 2026-10-19. `python3`, `curl` i `sha256sum` są tam niezweryfikowane, a T1 dokłada serwer TCP w Pythonie. Rekomendacja: w S7 przypiąć `runs-on: ubuntu-24.04`, a e2e na starcie sprawdza `command -v` i podaje czytelny błąd.

## 8. Ryzyka

- **Dwie implementacje budowania zapytania (JS i Go).** Chronią je wspólne wektory i pełna lista białych znaków. Poza kontraktem są samotne surogaty (JS rzuca `URIError`).
- **Scalanie trójstronne.** Równoległa zmiana tej samej jednostki (zapytanie, środowisko, definicja) zostawia wersję późniejszego zapisu i toast. Lista `vars` środowiska jest jedną jednostką.
- **Zmiany zachowania** (7.1, `### Zmienione`). Wejście po nazwie firmowej wymaga `-allow-host`; łagodzi to `*` i jasny komunikat 403.
- **Po każdym restarcie `{{token}}` ma wartość z tabeli albo zostaje dosłowny**, dopóki użytkownik nie włączy harmonogramu (odp. 3). Łagodzą to toast przy Send i toast o restarcie.
- **Harmonogram wysyła zapisaną wersję zapytania.** Łagodzi to hint przy `#dirty`.
- **RE2** nie ma lookaround ani backreference. Błąd widać w `problems` od razu po zapisie.
- **Tryb `0.0.0.0` bez uwierzytelniania.** Tokeny, log TCP i wysyłka bajtów są dostępne dla całej sieci (pytanie 1).
- **`.local` nie działa w binarkach linuksowych** (pytanie 2).
- **Wspólne sesje TCP.** Jedna osoba może rozłączyć sesję innej albo przerwać jej czekanie. Urządzenie z jednym klientem zostaje zajęte po zamknięciu kart.
- **Wait for reply** może wziąć za odpowiedź niezamówiony komunikat, który zaczął napływać po `t0`.
- **Send czeka do 20 s** przy zawieszonym serwerze tokenu (klient przerywa po 25 s).
- **Dwie binarki na jednym pliku** odświeżają tokeny niezależnie.
- **Zapomniany włączony harmonogram** loguje się co kilka minut, dopóki program działa. Widać to po liczniku na `#schedBtn`.
- **Windows:** bez `TCP_USER_TIMEOUT` czas wykrycia martwego peera z danymi w locie jest nieznany do pomiaru.
- **`ui.html` urośnie do około 2870 linii** w jednym `<script>`. Największe ryzyko regresji niosą `persistNow` ze scalaniem, `syncIfBehind`, `sendFlow`, globalny handler klawiszy i `setSideTab`.
- **Weryfikacja jest lokalna.** `-race` i e2e działają na Windows, a ścieżki linuksowe dopiero w CI, Dockerze albo na Pi. Opublikowanego wydania nie da się wycofać.

## 9. Pytania do użytkownika

Żadne z pytań nie blokuje startu prac (S1..S6). Rozstrzygają tylko zakres wydania.

1. **Sieć przy `-listen 0.0.0.0`**
   - A: sieć jest zaufana (tylko Twój zespół). Program działa bez hasła, a README ostrzega.
   - B: do sieci mają dostęp inne osoby lub urządzenia (np. sieć firmowa lub szpitalna). Wtedy w 1.2.0 dochodzi hasło dostępu `-password`: logowanie, cookie `HttpOnly`/`SameSite=Strict`, sprawdzane w `guard` także dla strumienia, około +1 dnia.
   - **Rekomendacja: A.** Domyślny nasłuch to 127.0.0.1, a dziś ta sama sieć i tak widzi hasła w `/api/state`.
2. **Nazwy `.local` na Linuksie lub Raspberry Pi**
   - A: serwer tokenu i urządzenia wpisujesz po IP albo po nazwie z DNS sieci. UI pokaże hint, a README to opisze.
   - B: wpisujesz je po nazwach `.local`, a binarka działa na Linuksie lub Pi. Wtedy dochodzi własny resolver mDNS bez zależności, także dla HTTP (około +1 dnia, osobny PR w 1.3.0 albo 1.2.x).
   - **Rekomendacja: A.** Na Windows nazwy `.local` działają, a wysyłka multicast przez kilka interfejsów to osobne ryzyko.

Rozstrzygnięte w planie bez pytania (wartości domyślne):
- wejście z sieci po IP i nazwach lokalnych, inne nazwy przez `-allow-host`, `*` przywraca dzisiejsze zachowanie;
- urządzenie TCP: preset None, a automatyczny ACK i korelacja odpowiedzi są w „Później”. Jeśli znasz urządzenie albo masz pierwsze bajty komunikatu, posłużą do ręcznego testu w T2.