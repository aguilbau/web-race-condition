# web-race-condition

A tool to test **web server race conditions**.

It opens multiple TCP/TLS connections, sends the full HTTP request
except the final trigger byte, then releases that last byte
**simultaneously** across all goroutines.  
This can provoke concurrent execution of the same server-side logic
(e.g. multiple DB inserts).

## Usage

```
go build -o web-race-condition main.go
./web-race-condition -host 127.0.0.1 -file request.txt
```

### Flags

```
-goroutines : number of goroutines (default 20)
-file       : file containing the request (- for stdin)
-host       : hostname (required)
-port       : port (default 80 or 443 depending on -https)
-https      : enable TLS
-preflush   : microsecond delay before barrier to help split TLS writes (default 20)
-jitter     : random jitter ±µs before sending the last byte (default 0)
```

## Limitations

- HTTPS / TLS
  - The last byte may be coalesced into the same TLS record, even with the two writes  
    The `-preflush` parameter reduces this risk but does not guarantee separation
  - Client forces HTTP/1.1 using ALPN, but if the server enforces HTTP/2 or a proxy intervenes, behavior may be unexpected

- Load balancers and proxies
  - If TLS is terminated upstream (LB, CDN, proxy), the simultaneity achieved at the edge can be diluted before reaching the backend
  - Load balancers may reorder or buffer requests, defeating the barrier effect

- HTTP/1.1 only
  - The trigger offset logic supports requests with `Content-Length`
  - Chunked requests or HTTP/2 are not supported

- Timing
  - `-jitter = 0` maximizes strictly simultaneous collisions
  - `-jitter > 0` fuzzes timing windows (useful to explore subtle races, but reduces perfect collisions)

## Recommendations

- For the sharpest trigger: test in plain HTTP directly on the backend
- For TLS: increase `-preflush` (100–300 µs) if records are still merged

## Example

A demo server is provided in the `example` directory  
It exposes two routes:

- `/insecure/{user}/transfer`  
  Performs a SELECT to check the balance, then two UPDATEs to debit and credit  
  This is vulnerable because two requests can both see the same balance before applying the debit.  
  Example: if Alice has 100, two concurrent transfers of 100 may both succeed, leaving Alice with -100.

- `/secure/{user}/transfer`  
  Uses an immediate transaction with a conditional update (`money >= amount`).  
  This prevents double-spending, because only one transaction can succeed in debiting the account.

Start the server:
```
cd example
./main.py
```

Run the tool against the insecure endpoint:
```
./web-race-condition -host 127.0.0.1 -port 8000 -file example/request_insecure.txt
```
Alice will end up with a negative balance :
```
Balances: alice=-80, bob=280
```

Restart the server to get a fresh db then run against the secure endpoint:
```
./web-race-condition -host 127.0.0.1 -port 8000 -file example/request_secure.txt
```
Here, no money was created
```
Balances: alice=0, bob=200
```
