# Conversation style
## Init promt
```
Мне нужно создать SLB (server load balancer).
L4-балансировщик без TLS-терминации:
Необходимо начать с L4 TCP-проксирования на Go-lang с динамической конфигурацией (KV-store / JSON-файлы) и системным тюнингом через Bash.
Заложить возможность доработки фич (UDP/SNI).
По итогам у меня в топологии должно быть несколько debian/linux машин (реальных или виртуальных) в статусах Active и Backup соответственно!
Также необходимо организовать мини интерфейс (пока что только CLI) для настройки.
Учитывай поставку, что это будет за сборка?
Сгенерируй мне AGENTS.md на основе примеров из официального источника https://agents.md/#examples а также лучших практик использования и разработки Load Balancer.
```
OUTPUT: AGENT.md

## Reconcile product
```
Where it can make sense" How about that idea: end users creates home servers or clusters (AI/K8S/etc) and serves services behind the router and our SLB come into place? but you can imagine more specific and more profitable cases for my LB/SLB product!
```
OUTPUT: PLANS.md, architecture.md, README.md, skeleton.md

ANSWER:
```
Small and mid-size on-prem businesses wanting HA without a network team. They have two servers and a few TCP services (RDP, SMTP, databases, legacy apps). The sellable value is operations: CLI plus a simple UI, drain-and-upgrade workflows, config sync, clear status. Money would come from support, install, or a managed offering, not licenses.
```