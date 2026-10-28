-- ============================================================================
--  discord_clone — демо-данные
--  Запускать ТОЛЬКО на чистой БД (после 001_init.sql).
--  Цель: увидеть, как связаны таблицы, и потрогать реальные запросы.
--
--  Пароли — заглушки, а не валидные argon2-хэши. Войти с ними нельзя.
--  Идентификаторы фиксированные, чтобы можно было ссылаться в curl/psql.
-- ============================================================================

BEGIN;

-- ---------------------------------------------------------------- пользователи
INSERT INTO users (id, username, display_name, email, password_hash) VALUES
    ('11111111-1111-1111-1111-111111111111', 'alice', 'Алиса',
     'alice@example.com', '$argon2id$v=19$m=65536,t=3,p=4$demo-alice'),
    ('22222222-2222-2222-2222-222222222222', 'bob',   'Боб',
     'bob@example.com',   '$argon2id$v=19$m=65536,t=3,p=4$demo-bob'),
    ('33333333-3333-3333-3333-333333333333', 'carol', 'Кэрол',
     'carol@example.com', '$argon2id$v=19$m=65536,t=3,p=4$demo-carol');

-- ---------------------------------------------------------------- сервер
INSERT INTO guilds (id, name, owner_id) VALUES
    ('aaaaaaaa-0000-0000-0000-000000000001', 'Go-разработчики',
     '11111111-1111-1111-1111-111111111111');

INSERT INTO guild_members (guild_id, user_id) VALUES
    ('aaaaaaaa-0000-0000-0000-000000000001', '11111111-1111-1111-1111-111111111111'),
    ('aaaaaaaa-0000-0000-0000-000000000001', '22222222-2222-2222-2222-222222222222'),
    ('aaaaaaaa-0000-0000-0000-000000000001', '33333333-3333-3333-3333-333333333333');

-- ---------------------------------------------------------------- роли
-- 1915 = VIEW_CHANNEL|SEND_MESSAGES|EMBED_LINKS|ATTACH_FILES|ADD_REACTIONS
--        |READ_MESSAGE_HISTORY|CONNECT|SPEAK|VIDEO
INSERT INTO roles (id, guild_id, name, color, position, permissions, is_default, hoist) VALUES
    ('bbbbbbbb-0000-0000-0000-000000000000', 'aaaaaaaa-0000-0000-0000-000000000001',
     '@everyone', 0, 0, 1915, true, false);

-- 919423 = 1915 | MANAGE_MESSAGES | KICK_MEMBERS | BAN_MEMBERS | CREATE_INVITE
INSERT INTO roles (id, guild_id, name, color, position, permissions, is_default, hoist) VALUES
    ('bbbbbbbb-0000-0000-0000-000000000001', 'aaaaaaaa-0000-0000-0000-000000000001',
     'Модератор', 3447003, 10, 919423, false, true);

-- 1048576 = ADMINISTRATOR (обходит все overwrite'ы)
INSERT INTO roles (id, guild_id, name, color, position, permissions, is_default, hoist) VALUES
    ('bbbbbbbb-0000-0000-0000-000000000002', 'aaaaaaaa-0000-0000-0000-000000000001',
     'Админ', 15158332, 20, 1048576, false, true);

INSERT INTO member_roles (guild_id, user_id, role_id) VALUES
    ('aaaaaaaa-0000-0000-0000-000000000001', '22222222-2222-2222-2222-222222222222',
     'bbbbbbbb-0000-0000-0000-000000000001'),   -- bob   → Модератор
    ('aaaaaaaa-0000-0000-0000-000000000001', '11111111-1111-1111-1111-111111111111',
     'bbbbbbbb-0000-0000-0000-000000000002');   -- alice → Админ

-- ---------------------------------------------------------------- каналы
INSERT INTO channels (id, guild_id, parent_id, type, name, position) VALUES
    ('cccccccc-0000-0000-0000-000000000000', 'aaaaaaaa-0000-0000-0000-000000000001',
     NULL, 'category', 'Основное', 0);

INSERT INTO channels (id, guild_id, parent_id, type, name, topic, position) VALUES
    ('cccccccc-0000-0000-0000-000000000001', 'aaaaaaaa-0000-0000-0000-000000000001',
     'cccccccc-0000-0000-0000-000000000000', 'text', 'general',
     'Общий канал. Правила — в закрепе.', 1),
    ('cccccccc-0000-0000-0000-000000000002', 'aaaaaaaa-0000-0000-0000-000000000001',
     'cccccccc-0000-0000-0000-000000000000', 'text', 'random',
     'Флуд и офтоп', 2);

INSERT INTO channels (id, guild_id, type, name, position, bitrate, user_limit) VALUES
    ('cccccccc-0000-0000-0000-000000000003', 'aaaaaaaa-0000-0000-0000-000000000001',
     'voice', 'Голосовой', 3, 64000, 25);

-- Пример overwrite: в #random писать могут только модераторы и админы.
-- deny SEND_MESSAGES (1<<1 = 2) для @everyone.
INSERT INTO channel_overwrites (channel_id, target_type, target_id, allow, deny) VALUES
    ('cccccccc-0000-0000-0000-000000000002', 'role',
     'bbbbbbbb-0000-0000-0000-000000000000', 0, 2);

-- ---------------------------------------------------------------- сообщения
INSERT INTO messages (id, channel_id, author_id, content) VALUES
    ('dddddddd-0000-0000-0000-000000000001',
     'cccccccc-0000-0000-0000-000000000001',
     '11111111-1111-1111-1111-111111111111',
     'Привет! Это первый канал нашего сервера.'),
    ('dddddddd-0000-0000-0000-000000000002',
     'cccccccc-0000-0000-0000-000000000001',
     '22222222-2222-2222-2222-222222222222',
     'Привет. Голосовой уже поднят?');

INSERT INTO messages (id, channel_id, author_id, content, type, reply_to_id) VALUES
    ('dddddddd-0000-0000-0000-000000000003',
     'cccccccc-0000-0000-0000-000000000001',
     '11111111-1111-1111-1111-111111111111',
     'Да, LiveKit запущен, заходи в #Голосовой.',
     'reply',
     'dddddddd-0000-0000-0000-000000000002');

-- Системное сообщение: author_id NULL
INSERT INTO messages (id, channel_id, author_id, content, type) VALUES
    ('dddddddd-0000-0000-0000-000000000004',
     'cccccccc-0000-0000-0000-000000000001',
     NULL,
     'carol присоединилась к серверу.',
     'system');

INSERT INTO reactions (message_id, user_id, emoji) VALUES
    ('dddddddd-0000-0000-0000-000000000001', '22222222-2222-2222-2222-222222222222', '👍'),
    ('dddddddd-0000-0000-0000-000000000001', '33333333-3333-3333-3333-333333333333', '👍');

-- ---------------------------------------------------------------- прочее
INSERT INTO read_states (user_id, channel_id, last_read_seq) VALUES
    ('11111111-1111-1111-1111-111111111111', 'cccccccc-0000-0000-0000-000000000001', 3);

INSERT INTO invites (code, guild_id, channel_id, inviter_id, max_uses) VALUES
    ('aBc123', 'aaaaaaaa-0000-0000-0000-000000000001',
     'cccccccc-0000-0000-0000-000000000001',
     '11111111-1111-1111-1111-111111111111', 10);

-- Завершённый голосовой сеанс
INSERT INTO voice_sessions (channel_id, user_id, joined_at, left_at, self_mute) VALUES
    ('cccccccc-0000-0000-0000-000000000003', '22222222-2222-2222-2222-222222222222',
     now() - interval '2 hours', now() - interval '1 hour', false);

COMMIT;

-- ============================================================================
--  Проверочные запросы (раскомментировать по одному):
--
--  -- последние сообщения канала с автором и числом реакций
--  SELECT m.seq, u.username, m.content, m.created_at,
--         count(r.*) FILTER (WHERE r.user_id IS NOT NULL) AS reactions
--    FROM messages m
--    LEFT JOIN users u ON u.id = m.author_id
--    LEFT JOIN reactions r ON r.message_id = m.id
--   WHERE m.channel_id = 'cccccccc-0000-0000-0000-000000000001'
--     AND m.deleted_at IS NULL
--   GROUP BY m.id, u.username
--   ORDER BY m.seq DESC
--   LIMIT 50;
--
--  -- все роли участника с их правами
--  SELECT u.username, r.name, r.permissions, r.position
--    FROM guild_members gm
--    JOIN users u ON u.id = gm.user_id
--    LEFT JOIN member_roles mr ON mr.guild_id = gm.guild_id AND mr.user_id = gm.user_id
--    LEFT JOIN roles r ON r.id = mr.role_id
--   WHERE gm.guild_id = 'aaaaaaaa-0000-0000-0000-000000000001'
--   ORDER BY u.username, r.position DESC;
--
--  -- поиск по сообщениям
--  SELECT content FROM messages
--   WHERE search @@ to_tsquery('simple', 'LiveKit');
-- ============================================================================
