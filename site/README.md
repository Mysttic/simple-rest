# Strona projektu na hubie Mysttic/Mysttic

Katalog `simple-rest/` to gotowa strona dla publicznego repozytorium
[Mysttic/Mysttic](https://github.com/Mysttic/Mysttic) (https://mysttic.github.io/Mysttic/),
ułożona jak `chatt/`: korzysta ze wspólnych `assets/style.css`, `assets/theme.js`
i `assets/releases.js`, a jedyny kod per projekt to wywołanie `initDownloads()` na końcu.

Podpięcie (jednorazowo, w repozytorium Mysttic/Mysttic):

1. Skopiować `site/simple-rest/` jako `simple-rest/` do katalogu głównego huba.
2. Dodać kartę w głównym `index.html`, obok pozostałych projektów:

```html
    <a class="project" href="simple-rest/">
      <div class="head">
        <img src="simple-rest/img/icon.svg" alt="">
        <h2>simple-rest</h2>
      </div>
      <p>
        A REST client that is one file. Collections, environments, history and
        a response viewer in your browser, requests sent by the program, so
        CORS never gets in the way. No installer, no account.
      </p>
      <div class="platforms">
        <span>Windows</span>
        <span>Linux</span>
        <span>Raspberry Pi</span>
        <span>macOS</span>
        <span>Android</span>
      </div>
      <span class="go">See it and download →</span>
    </a>
```

3. Dopisać wiersz do tabeli w `RELEASES.md`:

```
| simple-rest | `simple-rest/` | `simple-rest-v` |
```

Pliki release trafiają na hub automatycznie z workflow `Release` w tym repozytorium
(job `site`), pod tagiem `simple-rest-v<wersja>`; strona nie wymaga commitów przy
kolejnych wydaniach.
