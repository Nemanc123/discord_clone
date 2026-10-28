#!/usr/bin/env bash
# Проверка схемы и ограничений БД.
#
# Работает на ОТДЕЛЬНОЙ базе discord_clone_check: рабочая база не трогается.
# Требует запущенного контейнера dc_postgres.
#
# Запуск (из WSL):
#   cd /mnt/c/Users/user/discord_clone
#   bash scripts/db-check.sh
#
# Проверяет:
#   1. схема применяется на чистой базе;
#   2. демо-данные заливаются;
#   3. каждое ограничение действительно отклоняет неверные данные.

set -u
cd "$(dirname "$0")/.." || exit 1

CHECK_DB="discord_clone_check"
ADMIN="docker exec -i dc_postgres psql -U dc -d postgres -v ON_ERROR_STOP=1"
PSQL="docker exec -i dc_postgres psql -U dc -d $CHECK_DB -v ON_ERROR_STOP=1"

echo "=== пересоздаю отдельную базу $CHECK_DB ==="
$ADMIN -q -c "DROP DATABASE IF EXISTS $CHECK_DB" >/dev/null
$ADMIN -q -c "CREATE DATABASE $CHECK_DB" >/dev/null

if ! $PSQL -q -f - < db/001_init.sql >/dev/null 2>&1; then
    echo "СХЕМА НЕ ПРИМЕНИЛАСЬ"
    $PSQL -f - < db/001_init.sql 2>&1 | tail -5
    exit 1
fi
echo "схема: ok"

if ! $PSQL -q -f - < db/002_seed.sql >/dev/null 2>&1; then
    echo "ДЕМО-ДАННЫЕ НЕ ЗАЛИЛИСЬ"
    $PSQL -f - < db/002_seed.sql 2>&1 | tail -5
    exit 1
fi
echo "данные: ok"

echo
echo "=== структура ==="
$PSQL -tAc "select 'таблиц: ' || count(*) from information_schema.tables where table_schema='public'"
$PSQL -tAc "select 'индексов: ' || count(*) from pg_indexes where schemaname='public'"
$PSQL -tAc "select 'CHECK: ' || count(*) from pg_constraint c join pg_namespace n on n.oid=c.connamespace where n.nspname='public' and contype='c'"
$PSQL -tAc "select 'FK: ' || count(*) from pg_constraint c join pg_namespace n on n.oid=c.connamespace where n.nspname='public' and contype='f'"

echo
echo "=== ограничения: ожидаем отказ ==="
passed=0
failed=0

neg() {
    local name="$1" sql="$2"
    if $PSQL -q -c "begin; $sql; rollback;" >/dev/null 2>&1; then
        echo "  ПРОВАЛ  $name — база приняла неверные данные"
        failed=$((failed + 1))
    else
        echo "  ок      $name"
        passed=$((passed + 1))
    fi
}

pos() {
    local name="$1" sql="$2"
    if $PSQL -q -c "begin; $sql; rollback;" >/dev/null 2>&1; then
        echo "  ок      $name"
        passed=$((passed + 1))
    else
        echo "  ПРОВАЛ  $name — база отклонила верные данные"
        failed=$((failed + 1))
    fi
}

neg "username с заглавными" \
    "insert into users(username,password_hash) values ('BadName','x')"
neg "username из одного символа" \
    "insert into users(username,password_hash) values ('a','x')"
neg "дубль реакции" \
    "insert into reactions(message_id,user_id,emoji) values ('dddddddd-0000-0000-0000-000000000001','22222222-2222-2222-2222-222222222222','👍')"
neg "вторая роль @everyone" \
    "insert into roles(guild_id,name,is_default) values ('aaaaaaaa-0000-0000-0000-000000000001','Ещё одна',true)"
neg "allow и deny одновременно" \
    "insert into channel_overwrites(channel_id,target_type,target_id,allow,deny) values ('cccccccc-0000-0000-0000-000000000001','role','bbbbbbbb-0000-0000-0000-000000000000',8,8)"
neg "канал сервера без guild_id" \
    "insert into channels(type,name) values ('text','без сервера')"
neg "DM с guild_id" \
    "insert into channels(guild_id,type,name) values ('aaaaaaaa-0000-0000-0000-000000000001','dm','лс')"
neg "второй живой голосовой сеанс" \
    "insert into voice_sessions(channel_id,user_id) values ('cccccccc-0000-0000-0000-000000000003','33333333-3333-3333-3333-333333333333');
     insert into voice_sessions(channel_id,user_id) values ('cccccccc-0000-0000-0000-000000000003','33333333-3333-3333-3333-333333333333')"

echo
echo "=== составной ключ: роль чужого сервера ==="
neg "роль чужого сервера в member_roles" \
    "insert into guilds(id,name,owner_id) values ('bbbb0000-0000-0000-0000-000000000009','Другой','11111111-1111-1111-1111-111111111111');
     insert into roles(id,guild_id,name) values ('eeee0000-0000-0000-0000-000000000009','bbbb0000-0000-0000-0000-000000000009','Чужая');
     insert into member_roles(guild_id,user_id,role_id) values ('aaaaaaaa-0000-0000-0000-000000000001','33333333-3333-3333-3333-333333333333','eeee0000-0000-0000-0000-000000000009')"
pos "роль своего сервера в member_roles" \
    "insert into member_roles(guild_id,user_id,role_id) values ('aaaaaaaa-0000-0000-0000-000000000001','33333333-3333-3333-3333-333333333333','bbbbbbbb-0000-0000-0000-000000000001')"
pos "второй сеанс после закрытия первого" \
    "insert into voice_sessions(channel_id,user_id) values ('cccccccc-0000-0000-0000-000000000003','33333333-3333-3333-3333-333333333333');
     update voice_sessions set left_at=now() where user_id='33333333-3333-3333-3333-333333333333' and left_at is null;
     insert into voice_sessions(channel_id,user_id) values ('cccccccc-0000-0000-0000-000000000003','33333333-3333-3333-3333-333333333333')"

echo
echo "=== убираю $CHECK_DB ==="
$ADMIN -q -c "DROP DATABASE IF EXISTS $CHECK_DB" >/dev/null

echo
echo "итого: $passed ок, $failed провалов"
[ "$failed" -eq 0 ] || exit 1
