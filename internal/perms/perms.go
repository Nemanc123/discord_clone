// Package perms — движок прав.
//
// Права хранятся битовой маской в bigint. Одна такая маска на роль,
// одна на участника, одна на канал-переопределение.
//
// Порядок вычисления повторяет Discord, и порядок здесь принципиален:
// сначала база, потом переопределение @everyone, потом роли, потом участник.
package perms

// Permission — набор прав. 64 бита хватит с большим запасом.
type Permission int64

const (
	ViewChannel Permission = 1 << iota
	SendMessages
	ManageMessages
	EmbedLinks
	AttachFiles
	AddReactions
	ReadMessageHistory
	MentionEveryone
	Connect
	Speak
	Video
	MuteMembers
	DeafenMembers
	MoveMembers
	ManageChannels
	ManageRoles
	ManageGuild
	KickMembers
	BanMembers
	CreateInvite
	Administrator
)

// All — все известные права, включая будущие.
// Используется для администратора: он обходит любые переопределения.
const All Permission = 1<<21 - 1

// DefaultEveryone — что получает обычный участник сервера по умолчанию.
// Ровно то, без чего сервер бесполезен: читать, писать, говорить.
const DefaultEveryone = ViewChannel | SendMessages | EmbedLinks |
	AttachFiles | AddReactions | ReadMessageHistory |
	Connect | Speak | Video | CreateInvite

// Has проверяет наличие права с учётом администратора.
func (p Permission) Has(want Permission) bool {
	if p&Administrator != 0 {
		return true
	}
	return p&want == want
}

// IsAdmin — есть ли бит администратора. Отличается от Has(Administrator):
// Has вернёт true для любого права, а этот метод — только для самого бита.
func (p Permission) IsAdmin() bool {
	return p&Administrator != 0
}

// With возвращает маску с добавленными правами.
func (p Permission) With(extra Permission) Permission {
	return p | extra
}

// Without снимает права.
func (p Permission) Without(extra Permission) Permission {
	return p &^ extra
}

// Overwrite — переопределение прав на конкретный канал.
// Allow добавляет, Deny снимает. Одновременно стоять не могут
// (в БД это гарантирует CHECK).
type Overwrite struct {
	Allow Permission
	Deny  Permission
}

// Context — всё, что нужно для вычисления прав участника в канале.
type Context struct {
	// Base — сумма прав @everyone и всех ролей участника.
	Base Permission
	// Everyone — переопределение для роли @everyone на этом канале.
	Everyone Overwrite
	// Roles — переопределения ролей участника на этом канале.
	Roles []Overwrite
	// Member — персональное переопределение. Применяется последним.
	Member *Overwrite
}

// Compute вычисляет итоговые права.
//
// Правила:
//  1. Администратор получает всё и обходит переопределения.
//  2. На каждом уровне запрет применяется последним, поэтому deny всегда
//     сильнее allow — в том числе когда запрет и разрешение пришли от разных ролей.
//  3. Уровни применяются по порядку: @everyone → роли → участник.
//     Более конкретный уровень может вернуть снятое более общим.
//
// Отличие от Discord: там allow и deny всех ролей объединяются в одну маску
// и allow применяется последним, из-за чего разрешение одной роли перебивает
// запрет другой. Мы сознательно выбираем «запрет сильнее»: роль «Заглушён»
// должна работать, даже если у человека есть роль «Модератор».
func Compute(c Context) Permission {
	if c.Base&Administrator != 0 {
		return All
	}

	result := apply(c.Base, c.Everyone)

	var allow, deny Permission
	for _, role := range c.Roles {
		allow |= role.Allow
		deny |= role.Deny
	}
	result = apply(result, Overwrite{Allow: allow, Deny: deny})

	if c.Member != nil {
		result = apply(result, *c.Member)
	}

	return result
}

// apply добавляет разрешения и снимает запреты.
// Порядок важен: сначала allow, потом deny — поэтому запрет сильнее.
func apply(p Permission, o Overwrite) Permission {
	return (p | o.Allow) &^ o.Deny
}
