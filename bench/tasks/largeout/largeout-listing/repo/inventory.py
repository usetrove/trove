import json, sys

items = json.load(open("inventory.json"))
if "--validate" in sys.argv:
    sys.exit(0 if all(i["qty"] >= 0 for i in items) and len(items) == 300 else 1)
for i in items:
    print(f"{i['sku']}  {i['name']:<10}  qty={i['qty']:>3}  bin={i['bin']}")
print(f"{len(items)} items")
