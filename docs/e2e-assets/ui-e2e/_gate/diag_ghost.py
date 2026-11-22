import json, time
from playwright.sync_api import sync_playwright
BASE="http://127.0.0.1:8788"
def probe(pg, url, label):
    pg.goto(url, wait_until="domcontentloaded", timeout=60000)
    time.sleep(7)
    h1=pg.eval_on_selector_all("h1","e=>e.map(x=>(x.textContent||'').trim())")
    try: main=pg.inner_text("main")[:900]
    except Exception: main=""
    print(f"--- {label} ---  url={url.split('8788')[-1]}")
    print(" h1:", h1)
    print(" main:", main.replace("\n"," | ")[:450])
    return {"h1":h1,"main":main}
with sync_playwright() as p:
    b=p.chromium.launch(channel="chrome",headless=True)
    ctx=b.new_context(viewport={"width":1440,"height":950}); pg=ctx.new_page()
    real=probe(pg, f"{BASE}/#/colony/ui-e2e-012144", "REAL ui-e2e-012144")
    pg.screenshot(path="/Users/mac/hive/go_hive/docs/e2e-assets/ui-e2e/_gate/real.png")
    ghost=probe(pg, f"{BASE}/#/colony/ui-e2e-999999", "GHOST ui-e2e-999999")
    pg.screenshot(path="/Users/mac/hive/go_hive/docs/e2e-assets/ui-e2e/_gate/ghost.png")
    print("\n=== DISCRIMINATORS ===")
    print("real  main has queen attribution:", "Growth" in real["main"])
    print("ghost main has queen attribution:", "Growth" in ghost["main"])
    print("real  main non-trivial:", len(real["main"].strip())>150)
    print("ghost main non-trivial:", len(ghost["main"].strip())>150)
    b.close()
