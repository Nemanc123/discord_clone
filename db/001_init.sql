-- ============================================================================
--  discord_clone — начальная схема
--  PostgreSQL 16+
--  Применение:  psql -d discord_clone -f db/001_init.sql
--
--  Порядок таблиц = порядок зависимостей. Менять местами нельзя.
--  Всё в одной транзакции: либо схема целиком, либо ничего.
-- ============================================================================

BEGIN;

CREATE EXTENSION IF NOT EXISTS citext;      -- регистронезависимые username/email
CREATE EXTENSION IF NOT EXISTS pgcrypto;    -- gen_random_uuid() на PG < 13

-- ---------------------------------------------------------------------------
--  Общий триггер: обновлять updated_at при UPDATE
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION set_updated_at() RETURNS trigger
LANGUAGE plpgsql AS $$
BEGIN
    NEW.updated_at := now();
    RETURN NEW;
END;
$$;


-- ===========================================================================
--  1. ПОЛЬЗОВАТЕЛИ И СЕССИИ
-- ===========================================================================

CREATE TABLE users (
    id            uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    username      citext      NOT NULL UNIQUE,
    display_name  text,
    email         citext      UNIQUE,
    password_hash text        NOT NULL,          -- argon2id, формат $argon2id$v=19$...
    avatar_url    text,
    status        text        NOT NULL DEFAULT 'offline'
                              CHECK (status IN ('online','idle','dnd','offline')),
    is_bot        boolean     NOT NULL DEFAULT false,
    created_at    timestamptz NOT NULL DEFAULT now(),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    deleted_at    timestamptz,
    -- citext сравнивает без учёта регистра; для формата нужен явный text
    CONSTRAINT users_username_lowercase CHECK (username::text = lower(username::text)),
    CONSTRAINT users_username_format  CHECK (username::text ~ '^[a-z0-9_.]{2,32}$'),
    CONSTRAINT users_email_format
        CHECK (email IS NULL OR email::text ~ '^[^@[:space:]]+@[^@[:space:]]+\.[^@[:space:]]+$')
);

CREATE TRIGGER users_set_updated_at
    BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Сессии = refresh-токены. Access-токен (JWT) живёт 15 минут и в БД не пишется.
CREATE TABLE user_sessions (
    id           uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    user_id      uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    token_hash   bytea       NOT NULL,           -- sha256(refresh_token), не сам токен
    user_agent   text,
    ip           inet,
    created_at   timestamptz NOT NULL DEFAULT now(),
    last_seen_at timestamptz NOT NULL DEFAULT now(),
    expires_at   timestamptz NOT NULL,
    revoked_at   timestamptz,
    CONSTRAINT user_sessions_expiry CHECK (expires_at > created_at)
);

CREATE UNIQUE INDEX user_sessions_token_hash_key  ON user_sessions (token_hash);
CREATE INDEX        user_sessions_active_user_idx ON user_sessions (user_id)
    WHERE revoked_at IS NULL;


-- ===========================================================================
--  2. СЕРВЕРЫ (GUILDS), УЧАСТНИКИ, РОЛИ
-- ===========================================================================

CREATE TABLE guilds (
    id         uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    name       text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    icon_url   text,
    owner_id   uuid        NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
    created_at timestamptz NOT NULL DEFAULT now(),
    updated_at timestamptz NOT NULL DEFAULT now(),
    deleted_at timestamptz
);

CREATE TRIGGER guilds_set_updated_at
    BEFORE UPDATE ON guilds
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

CREATE INDEX guilds_owner_id_idx ON guilds (owner_id);

CREATE TABLE guild_members (
    guild_id  uuid        NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    user_id   uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    nickname  text        CHECK (nickname IS NULL OR char_length(nickname) BETWEEN 1 AND 32),
    joined_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (guild_id, user_id)
);

-- «все серверы пользователя»: нужен на каждом логине (READY)
CREATE INDEX guild_members_user_id_idx ON guild_members (user_id);

CREATE TABLE roles (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    guild_id    uuid        NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    name        text        NOT NULL CHECK (char_length(name) BETWEEN 1 AND 100),
    color       integer     NOT NULL DEFAULT 0 CHECK (color BETWEEN 0 AND 16777215),
    position    integer     NOT NULL DEFAULT 0,     -- 0 = @everyone, выше = больше прав
    permissions bigint      NOT NULL DEFAULT 0 CHECK (permissions >= 0),
    is_default  boolean     NOT NULL DEFAULT false, -- @everyone
    hoist       boolean     NOT NULL DEFAULT false, -- показывать отдельной группой в списке
    mentionable boolean     NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    UNIQUE (guild_id, name),
    -- нужно для составного FK из member_roles: роль обязана быть из того же сервера.
    -- Порядок колонок ВАЖЕН: FK сопоставляет их позиционно, а не по имени,
    -- поэтому здесь ровно (guild_id, id) — как в членах member_roles.
    UNIQUE (guild_id, id)
);

CREATE UNIQUE INDEX roles_one_default_per_guild ON roles (guild_id) WHERE is_default;
CREATE INDEX        roles_guild_position_idx   ON roles (guild_id, position DESC);

CREATE TABLE member_roles (
    guild_id    uuid        NOT NULL,
    user_id     uuid        NOT NULL,
    role_id     uuid        NOT NULL,
    assigned_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (guild_id, user_id, role_id),
    FOREIGN KEY (guild_id, user_id) REFERENCES guild_members (guild_id, user_id) ON DELETE CASCADE,
    FOREIGN KEY (guild_id, role_id) REFERENCES roles (guild_id, id)           ON DELETE CASCADE
);

CREATE INDEX member_roles_role_id_idx ON member_roles (role_id);

CREATE TABLE guild_bans (
    guild_id  uuid        NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    user_id   uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    banned_by uuid        REFERENCES users(id) ON DELETE SET NULL,
    reason    text        CHECK (reason IS NULL OR char_length(reason) <= 512),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (guild_id, user_id)
);


-- ===========================================================================
--  3. КАНАЛЫ
--     type: text | voice | category | announcement | forum | dm | group_dm
--     DM — тоже канал, но без сервера. Так сообщения и права живут в одной
--     модели, а не в отдельной таблице личных сообщений.
-- ===========================================================================

CREATE TABLE channels (
    id               uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    guild_id         uuid REFERENCES guilds(id)   ON DELETE CASCADE,
    parent_id        uuid REFERENCES channels(id) ON DELETE CASCADE,  -- category
    type             text NOT NULL
                     CHECK (type IN ('text','voice','category','announcement','forum','dm','group_dm')),
    name             text CHECK (name IS NULL OR char_length(name) BETWEEN 1 AND 100),
    topic            text CHECK (topic IS NULL OR char_length(topic) <= 1024),
    position         integer NOT NULL DEFAULT 0,
    is_nsfw          boolean NOT NULL DEFAULT false,
    slowmode_seconds integer NOT NULL DEFAULT 0 CHECK (slowmode_seconds BETWEEN 0 AND 21600),
    bitrate          integer,      -- только voice, бит/с
    user_limit       integer,      -- только voice, 0 = без лимита
    dm_key           text,         -- dm/group_dm: отсортированный набор user_id, для поиска дублей
    created_at       timestamptz NOT NULL DEFAULT now(),
    updated_at       timestamptz NOT NULL DEFAULT now(),
    deleted_at       timestamptz,
    -- канал сервера обязан иметь guild_id; DM обязан его не иметь
    CONSTRAINT channels_guild_required
        CHECK ((type IN ('dm','group_dm')) = (guild_id IS NULL)),
    CONSTRAINT channels_name_required
        CHECK (type IN ('dm','group_dm') OR name IS NOT NULL),
    CONSTRAINT channels_voice_options
        CHECK (type = 'voice' OR (bitrate IS NULL AND user_limit IS NULL)),
    CONSTRAINT channels_voice_bitrate
        CHECK (bitrate IS NULL OR bitrate BETWEEN 8000 AND 384000),
    CONSTRAINT channels_user_limit
        CHECK (user_limit IS NULL OR user_limit BETWEEN 0 AND 500)
);

CREATE TRIGGER channels_set_updated_at
    BEFORE UPDATE ON channels
    FOR EACH ROW EXECUTE FUNCTION set_updated_at();

-- Уникальность имени среди соседей. COALESCE нужен, потому что NULL != NULL
-- в уникальном индексе, и два канала верхнего уровня с одним именем прошли бы.
CREATE UNIQUE INDEX channels_guild_name_key
    ON channels (guild_id, COALESCE(parent_id, '00000000-0000-0000-0000-000000000000'::uuid), lower(name))
    WHERE deleted_at IS NULL AND guild_id IS NOT NULL;

CREATE UNIQUE INDEX channels_dm_key_key ON channels (dm_key) WHERE dm_key IS NOT NULL;
CREATE INDEX channels_guild_position_idx ON channels (guild_id, position) WHERE guild_id IS NOT NULL;

-- Состав DM/group_dm. Для серверных каналов пусто.
CREATE TABLE channel_recipients (
    channel_id uuid        NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES users(id)   ON DELETE CASCADE,
    added_at   timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (channel_id, user_id)
);

CREATE INDEX channel_recipients_user_id_idx ON channel_recipients (user_id);

-- Переопределения прав. target_id полиморфный (роль или участник),
-- поэтому внешнего ключа нет — целостность держит приложение.
CREATE TABLE channel_overwrites (
    channel_id  uuid   NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    target_type text   NOT NULL CHECK (target_type IN ('role','member')),
    target_id   uuid   NOT NULL,
    allow       bigint NOT NULL DEFAULT 0 CHECK (allow >= 0),
    deny        bigint NOT NULL DEFAULT 0 CHECK (deny >= 0),
    PRIMARY KEY (channel_id, target_type, target_id),
    CONSTRAINT channel_overwrites_disjoint CHECK ((allow & deny) = 0)
);


-- ===========================================================================
--  4. СООБЩЕНИЯ
--     seq — глобальный монотонный номер. По нему сортировка внутри канала
--     и курсорная пагинация. UUID — для публичных id.
-- ===========================================================================

CREATE TABLE messages (
    id          uuid   PRIMARY KEY DEFAULT gen_random_uuid(),
    seq         bigint GENERATED ALWAYS AS IDENTITY UNIQUE,
    channel_id  uuid   NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    author_id   uuid   REFERENCES users(id) ON DELETE SET NULL,   -- NULL = системное сообщение
    content     text   NOT NULL DEFAULT '' CHECK (char_length(content) <= 4000),
    type        text   NOT NULL DEFAULT 'default'
                       CHECK (type IN ('default','system','reply')),
    reply_to_id uuid   REFERENCES messages(id) ON DELETE SET NULL,
    is_pinned   boolean NOT NULL DEFAULT false,
    created_at  timestamptz NOT NULL DEFAULT now(),
    edited_at   timestamptz,
    deleted_at  timestamptz,
    -- полнотекстовый поиск; 'simple' — без морфологии, работает для любого языка.
    -- Для русского лучше 'russian', но тогда испортится поиск по никам/латинице.
    search      tsvector GENERATED ALWAYS AS (to_tsvector('simple', coalesce(content, ''))) STORED
);

-- Основной запрос: последние N сообщений канала
CREATE INDEX messages_channel_seq_idx ON messages (channel_id, seq DESC);
CREATE INDEX messages_author_idx      ON messages (author_id) WHERE author_id IS NOT NULL;
CREATE INDEX messages_reply_idx       ON messages (reply_to_id) WHERE reply_to_id IS NOT NULL;
CREATE INDEX messages_search_idx      ON messages USING gin (search);

CREATE TABLE attachments (
    id           uuid   PRIMARY KEY DEFAULT gen_random_uuid(),
    message_id   uuid   NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    filename     text   NOT NULL CHECK (char_length(filename) BETWEEN 1 AND 255),
    content_type text   NOT NULL,
    size_bytes   bigint NOT NULL CHECK (size_bytes > 0 AND size_bytes <= 26214400), -- 25 МБ
    storage_key  text   NOT NULL UNIQUE,     -- ключ объекта в MinIO/S3
    width        integer,
    height       integer,
    duration_ms  integer,
    created_at   timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX attachments_message_id_idx ON attachments (message_id);

CREATE TABLE reactions (
    message_id uuid        NOT NULL REFERENCES messages(id) ON DELETE CASCADE,
    user_id    uuid        NOT NULL REFERENCES users(id)    ON DELETE CASCADE,
    emoji      text        NOT NULL CHECK (char_length(emoji) BETWEEN 1 AND 64),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (message_id, user_id, emoji)
);

-- Отметки прочтения. Уровень — канал, как в Discord для серверных каналов.
-- last_read_seq не FK: это позиция в потоке, а не ссылка на строку
-- (сообщение могли удалить).
CREATE TABLE read_states (
    user_id       uuid        NOT NULL REFERENCES users(id)    ON DELETE CASCADE,
    channel_id    uuid        NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    last_read_seq bigint      NOT NULL DEFAULT 0 CHECK (last_read_seq >= 0),
    mention_count integer     NOT NULL DEFAULT 0 CHECK (mention_count >= 0),
    updated_at    timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, channel_id)
);

CREATE INDEX read_states_channel_idx ON read_states (channel_id);


-- ===========================================================================
--  5. ГОЛОС
--     Живое состояние (кто сейчас в канале) — в Redis.
--     Здесь только история подключений: для статистики и модерации.
-- ===========================================================================

CREATE TABLE voice_sessions (
    id          uuid PRIMARY KEY DEFAULT gen_random_uuid(),
    channel_id  uuid        NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    user_id     uuid        NOT NULL REFERENCES users(id)    ON DELETE CASCADE,
    joined_at   timestamptz NOT NULL DEFAULT now(),
    left_at     timestamptz,
    self_mute   boolean     NOT NULL DEFAULT false,
    self_deaf   boolean     NOT NULL DEFAULT false,
    self_video  boolean     NOT NULL DEFAULT false,
    self_stream boolean     NOT NULL DEFAULT false,
    CONSTRAINT voice_sessions_order CHECK (left_at IS NULL OR left_at >= joined_at)
);

-- Один пользователь не может быть дважды в одном канале одновременно
CREATE UNIQUE INDEX voice_sessions_live_key
    ON voice_sessions (channel_id, user_id) WHERE left_at IS NULL;

CREATE INDEX voice_sessions_channel_idx ON voice_sessions (channel_id, joined_at DESC);
CREATE INDEX voice_sessions_user_idx    ON voice_sessions (user_id, joined_at DESC);


-- ===========================================================================
--  6. ПРИГЛАШЕНИЯ, СВЯЗИ, НАСТРОЙКИ, АУДИТ
-- ===========================================================================

CREATE TABLE invites (
    code       text PRIMARY KEY CHECK (code ~ '^[A-Za-z0-9]{6,16}$'),
    guild_id   uuid        NOT NULL REFERENCES guilds(id)   ON DELETE CASCADE,
    channel_id uuid        NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
    inviter_id uuid        REFERENCES users(id) ON DELETE SET NULL,
    max_uses   integer     CHECK (max_uses IS NULL OR max_uses > 0),
    uses       integer     NOT NULL DEFAULT 0 CHECK (uses >= 0),
    expires_at timestamptz,
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX invites_guild_id_idx ON invites (guild_id);

-- Друзья и блокировки. Одна строка на направление; при чтении учитываем оба.
CREATE TABLE relationships (
    user_id    uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    target_id  uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    type       text        NOT NULL CHECK (type IN ('friend','pending_out','pending_in','blocked')),
    created_at timestamptz NOT NULL DEFAULT now(),
    PRIMARY KEY (user_id, target_id),
    CONSTRAINT relationships_no_self CHECK (user_id <> target_id)
);

CREATE INDEX relationships_target_idx ON relationships (target_id);

-- Настройки уведомлений: общие (scope 'user'), на сервер ('guild'),
-- на канал ('channel').
CREATE TABLE notification_settings (
    user_id     uuid        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    scope_type  text        NOT NULL CHECK (scope_type IN ('user','guild','channel')),
    scope_id    uuid        NOT NULL,
    level       text        NOT NULL CHECK (level IN ('all','mentions','nothing')),
    muted_until timestamptz,
    PRIMARY KEY (user_id, scope_type, scope_id),
    CONSTRAINT notification_settings_scope
        CHECK (scope_type <> 'user' OR scope_id = user_id)
);

CREATE TABLE audit_logs (
    id          bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    guild_id    uuid        NOT NULL REFERENCES guilds(id) ON DELETE CASCADE,
    actor_id    uuid        REFERENCES users(id) ON DELETE SET NULL,
    action      text        NOT NULL,     -- 'channel.create', 'member.ban', ...
    target_type text,
    target_id   uuid,
    changes     jsonb       NOT NULL DEFAULT '{}'::jsonb,
    created_at  timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX audit_logs_guild_idx ON audit_logs (guild_id, id DESC);

COMMIT;

-- ============================================================================
--  После применения:
--    psql -d discord_clone -f db/002_seed.sql     -- демо-данные
--
--  Известные ограничения схемы (осознанные):
--   1. channel_overwrites.target_id не имеет FK — полиморфизм.
--   2. Сообщение без текста (только вложение) проверяется в приложении,
--      а не в БД: attachment вставляется отдельным INSERT после message.
--   3. Глобальный messages.seq при переходе на шардирование придётся
--      заменить на локальный счётчик канала.
--   4. users.username остаётся занятым после удаления аккаунта (UNIQUE).
--      Если нужна переиспользуемость ников — отдельная миграция.
-- ============================================================================
