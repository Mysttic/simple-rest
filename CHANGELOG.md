# Changelog

Wszystkie istotne zmiany w projekcie. Format według [Keep a Changelog](https://keepachangelog.com/pl/1.1.0/),
numeracja według [SemVer](https://semver.org/lang/pl/). Numer bieżącej wersji jest w `VERSION.md`;
release powstaje po merge do `master`, gdy ten numer jest wyższy od ostatniego wydanego tagu.

## [Unreleased]

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
