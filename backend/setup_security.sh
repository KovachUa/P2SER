#!/bin/bash
set -e

echo "=== Налаштування безпечного середовища для P2SER ==="

# 1. Створення системного користувача без home-каталогу і без оболонки
if id "p2ser" &>/dev/null; then
    echo "Користувач p2ser вже існує."
else
    echo "Створюємо системного користувача p2ser..."
    sudo useradd --system --no-create-home --shell /bin/false p2ser
    echo "Користувач створений успішно."
fi

# 2. Компіляція проекту
echo "Збираємо бінарний файл..."
go mod tidy
go build -o p2ser

# 3. Призначення власника та прав доступу
echo "Налаштовуємо права на бінарник..."
sudo chown p2ser:p2ser ./p2ser
sudo chmod 750 ./p2ser

# 4. Видача CAP_NET_ADMIN (дозволяє створювати VXLAN і маршрути без sudo)
echo "Видаємо Linux Capabilities (CAP_NET_ADMIN)..."
sudo setcap cap_net_admin+ep ./p2ser

echo "=== Готово! ==="
echo "Тепер оркестратор може створювати мережі безпечно."
echo "Приклад запуску:"
echo "  sudo -u p2ser ./p2ser --name node1"
