# Render evidence — configuration / governance / release cadence

Rendered with Browser Use (Chromium), device emulation; screenshots at 2× DPR.

| page | desktop 1280×900 | mobile 390×844 | observed |
|---|---|---|---|
| `docs/configuration.html` | `configuration-desktop.png` | `configuration-mobile.png` | docScrollWidth = viewport (1280 / 390); no horizontal overflow; 26 tables scroll inside `.tablewrap` on mobile; 9 TOC links all resolve |
| `docs/delivery-governance.html` | `governance-desktop.png` | `governance-mobile.png` | after fix: docSW = 390, max card right edge 374, no text overflow; 7 cards, 4 tables |
| `docs/release-cadence.html` | `release-cadence-desktop.png` | `release-cadence-mobile.png` | docSW = 390; 2 tables, 31 rows; dates/slots readable |

## Bug found and fixed during this check

At 390px the governance G2 card contained a long slash-separated token
(`sessions/colonies/tracker/credentials/memories/senders/skills`), whose
min-content width forced the single-column grid to 380.56px and pushed the
card's right edge to 397px (7px page overflow). Fix: `.card .d` gets
`overflow-wrap:anywhere; word-break:break-word` in both the governance page
and the configuration generator. Re-rendered: `docSW=390`, `maxCardRight=374`,
`textOverflow=[]`.

Vision inspection of the mobile governance screenshot independently flagged
the same long-token risk; the DOM measurements above are the post-fix proof.
