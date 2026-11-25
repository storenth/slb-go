# BUILD
go build -o bin/slb ./cmd/slb
# Config json and run
./bin/slb run --config config/dev.json

# verisy LISTENER
sudo lsof -i -P | grep LISTEN
nc -zvv  127.0.0.1 8443

# prepare backends
mkdir -p /tmp/slb-a /tmp/slb-b
echo "backend A" > /tmp/slb-a/index.html
echo "backend B" > /tmp/slb-b/index.html
# run backends
cd /tmp/slb-a && openssl s_server -accept 9443 -cert $CERTPATH -key $KEYPATH -WWW
cd /tmp/slb-b && openssl s_server -accept 9444 -cert $CERTPATH -key $KEYPATH -WWW

# test client
for i in {1..10}; do curl -skS https://127.0.0.1:8443/index.html; done
openssl s_client -connect 127.0.0.1:8443 </dev/null 2>/dev/null | openssl x509 -noout -subject
# look at SLB connection stats
./bin/slb status
# stdout: round-robin logic
# backend A
# backend B
# backend A
# backend B
# subject=CN=slb-test-backend
# Uptime: 1077s

# Service: web  (0 active, 7 total)
#   Listener: 127.0.0.1:8443/tcp  0 active, 7 total
#   Backend:  127.0.0.1:9443  ejected 0 active, 4 total, 0 failed
#   Backend:  127.0.0.1:9444  up 0 active, 2 total, 1 failed


# test for idle in config: should closed faster then sleep
time openssl s_client -connect 127.0.0.1:8443 </dev/null < <(sleep 5) >/dev/null 2>&1
# test for idle in config: should closed faster then socket timeout (settimeout)
python3 - <<'PY'
import socket, time
s = socket.create_connection(("127.0.0.1", 8443))
t = time.time()
s.settimeout(15)
try:
    data = s.recv(1)
    print("closed by the SLB after %.1fs (recv returned %r)" % (time.time() - t, data))
except socket.timeout:
    print("NOT closed after 20s: idle is not enforced")
PY


# test backends with big files
dd if=/dev/zero of=/tmp/slb-a/big.bin bs=1m count=200
curl -sk -o /dev/null -w '%{size_download} bytes, %{speed_download} B/s\n' https://127.0.0.1:8443/big.bin
curl -sk -o /dev/null -w '%{size_download} bytes, %{speed_download} B/s\n' https://127.0.0.1:8443/big.bin
# stdout: we will miss file from backend B
# \n' https://127.0.0.1:8443/big.bin
# 209715200 bytes, 306222867 B/s
# \n' https://127.0.0.1:8443/big.bin
# 268 bytes, 8713 B/s
