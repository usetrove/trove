import csv, sys

RATES = {"EUR": 1.08, "USD": 1.0}


def main():
    done, total = 0, 0.0
    with open("orders.csv", newline="") as f:
        rows = list(csv.DictReader(f))
    for r in rows:
        if r["currency"] not in RATES:
            print(f"2026-11-25T10:00:{done % 60:02d} ERROR order {r['id']}: currency {r['currency']!r} unknown, skipped")
            continue
        usd = float(r["amount"]) * RATES[r["currency"]]
        total += usd
        done += 1
        print(f"2026-11-25T10:00:{done % 60:02d} INFO  order {r['id']} for {r['customer']} converted {r['amount']} {r['currency']} -> {usd:.2f} USD")
    print(f"processed {done} of {len(rows)} orders, total {total:.2f} USD")
    if "--check" in sys.argv and done != len(rows):
        sys.exit(1)


main()
