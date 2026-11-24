sudo lsof -i -P | grep LISTEN
nc -zvv  127.0.0.1 8443

mkdir -p /tmp/slb-a /tmp/slb-b
echo "backend A" > /tmp/slb-a/index.html
echo "backend B" > /tmp/slb-b/index.html

cd /tmp/slb-a && openssl s_server -accept 9443 -cert $CERTPATH -key $KEYPATH -WWW
cd /tmp/slb-b && openssl s_server -accept 9444 -cert $CERTPATH -key $KEYPATH -WWW

for i in 1 2 3 4; do curl -sk https://127.0.0.1:8443/index.html; done 
openssl s_client -connect 127.0.0.1:8443 </dev/null 2>/dev/null | openssl x509 -noout -subject
./bin/slb status --socket /tmp/slb-dev.sock
# stdout: round-robin logic
# backend A
# backend B
# backend A
# backend B
# subject=CN=slb-test-backend
# Uptime: 1077s

# Service: web  (0 active, 7 total)
#   Listener: 127.0.0.1/tcp  0 active, 7 total
#   Backend:  127.0.0.1:9443  0 active, 4 total
#   Backend:  127.0.0.1:9444  0 active, 3 total

dd if=/dev/zero of=/tmp/slb-a/big.bin bs=1m count=200
curl -sk -o /dev/null -w '%{size_download} bytes, %{speed_download} B/s\n' https://127.0.0.1:8443/big.bin
curl -sk -o /dev/null -w '%{size_download} bytes, %{speed_download} B/s\n' https://127.0.0.1:8443/big.bin
# stdout: we will miss file from backend B
# \n' https://127.0.0.1:8443/big.bin
# 209715200 bytes, 306222867 B/s
# \n' https://127.0.0.1:8443/big.bin
# 268 bytes, 8713 B/s
