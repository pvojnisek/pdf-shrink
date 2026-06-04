# pdf-shrink

Apró webes szolgáltatás: PDF feltöltése → méretcsökkentés → letöltés. Belső
hálózatra, auth nélkül. A kimenetek ~2 óráig elérhetők, utána automatikusan
törlődnek. Egyetlen statikus Go-bináris, nulla futásidejű függőség.

## Mit csinál

1. **Apple Markup / PencilKit strip** — törli a `/PPK` és minden `/AAPL…` kulcsot
   (ez okozta az eredeti 230 MB → 2,2 MB esetet). A látható jelölések
   (`/Stamp`, `/FreeText`, megjelenítő `/AP`) érintetlenül maradnak.
2. **Kép-downsampling** — a 2000px-nél nagyobb DeviceRGB/DeviceGray képeket
   lekicsinyíti és JPEG-ként (q80) újrakódolja. Biztonságból kihagyja a
   maszkos / indexelt / CMYK / prediktoros képeket, és csak akkor cseréli, ha
   tényleg fogyott tőle.
3. **Optimalizálás** — pdfcpu dedup + object streams + max flate tömörítés.

A weboldal a feltöltés mellett **listázza a még elérhető (le nem járt) fájlokat**
is, méret- és hátralévő-idő jelzéssel.

## Követelmények

- **Go 1.21+** a fordításhoz (fejlesztve: 1.26).
- Futtatáshoz semmi — a kész bináris statikus, függőség nélküli.

## Build

> A binárisok nincsenek a repóban (lásd `.gitignore`) — fordítsd magad:

```sh
go build -o pdf-shrink .                                              # aktuális platform
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -ldflags="-s -w" \
  -o pdf-shrink-linux-amd64 .                                        # linux/amd64 statikus
```

## Futtatás

```sh
# helyben (Mac, fejlesztés)
./pdf-shrink                  # http://localhost:8080
PORT=9000 ./pdf-shrink        # más port

# Linux szerver: a statikus bináris
./pdf-shrink-linux-amd64
```

## Telepítés a home szerverre (systemd)

```sh
sudo mkdir -p /opt/pdf-shrink
sudo cp pdf-shrink-linux-amd64 /opt/pdf-shrink/pdf-shrink
sudo cp pdf-shrink.service /etc/systemd/system/
sudo systemctl daemon-reload
sudo systemctl enable --now pdf-shrink
```

Aztán: `http://<szerver-ip>:8080`

## Hangolható konstansok

- `main.go`: `fileTTL` (2h lejárat), `maxUpload` (600 MB), `listenAddr` (`:8080`),
  `PORT` env-felülírás.
- `images.go`: `maxEdge` (2000px), `jpegQ` (80).

## Projekt felépítése

| Fájl | Szerep |
|---|---|
| `main.go` | HTTP-szerver, feltöltő/eredmény oldalak, fájllista, 2 órás takarítás |
| `shrink.go` | Feldolgozó pipeline (strip → kép → optimize → írás) |
| `images.go` | Kép-downsampler (DCTDecode + FlateDecode RGB/Gray) |
| `pdf-shrink.service` | systemd unit a szerverhez |

## Licenc

[MIT](LICENSE) © 2026 Peter Vojnisek
