import json
from playwright.sync_api import sync_playwright
res=[]
with sync_playwright() as p:
    for name,kw in (("channel-chrome",{"channel":"chrome"}),
                    ("execpath-chrome",{"executable_path":"/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"})):
        try:
            b=p.chromium.launch(headless=True, **kw)
            pg=b.new_page(); pg.goto("http://127.0.0.1:8788/", wait_until="load", timeout=25000)
            res.append({"try":name,"ok":True,"title":pg.title()}); b.close()
        except Exception as e:
            res.append({"try":name,"ok":False,"error":str(e)[:180]})
print(json.dumps(res,indent=2))
