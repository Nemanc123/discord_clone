package perms

import "testing"

// Табличные тесты: каждая строка — конкретная комбинация и ожидаемый результат.
// Именно они ловят ошибки в порядке применения правил, которые не видны
// при чтении кода.

func TestCompute(t *testing.T) {
	const (
		send = SendMessages
		view = ViewChannel
	)

	tests := []struct {
		name string
		ctx  Context
		want Permission
	}{
		{
			name: "только база",
			ctx:  Context{Base: view | send},
			want: view | send,
		},
		{
			name: "пустой контекст",
			ctx:  Context{},
			want: 0,
		},
		{
			name: "администратор обходит всё",
			ctx: Context{
				Base:     Administrator,
				Everyone: Overwrite{Deny: view | send},
				Roles:    []Overwrite{{Deny: view | send}},
				Member:   &Overwrite{Deny: view | send},
			},
			want: All,
		},
		{
			name: "администратор из роли тоже обходит переопределения",
			ctx: Context{
				Base:     view | Administrator,
				Everyone: Overwrite{Deny: view},
			},
			want: All,
		},
		{
			name: "deny у @everyone снимает право",
			ctx: Context{
				Base:     view | send,
				Everyone: Overwrite{Deny: send},
			},
			want: view,
		},
		{
			name: "allow у @everyone добавляет право",
			ctx: Context{
				Base:     view,
				Everyone: Overwrite{Allow: send},
			},
			want: view | send,
		},
		{
			name: "deny роли снимает право,данное базой",
			ctx: Context{
				Base:  view | send,
				Roles: []Overwrite{{Deny: send}},
			},
			want: view,
		},
		{
			name: "allow роли добавляет право",
			ctx: Context{
				Base:  view,
				Roles: []Overwrite{{Allow: send}},
			},
			want: view | send,
		},
		{
			name: "запрет одной роли не перебивается разрешением другой",
			ctx: Context{
				Base: view | send,
				Roles: []Overwrite{
					{Deny: send},
					{Allow: send},
				},
			},
			want: view,
		},
		{
			name: "запрет @everyone не перебивается разрешением роли",
			ctx: Context{
				Base:     view | send,
				Everyone: Overwrite{Deny: send},
				Roles:    []Overwrite{{Allow: send}},
			},
			want: view | send,
		},
		{
			name: "разрешение участника возвращает снятое ролями право",
			ctx: Context{
				Base:   view | send,
				Roles:  []Overwrite{{Deny: send}},
				Member: &Overwrite{Allow: send},
			},
			want: view | send,
		},
		{
			name: "запрет участника сильнее разрешения роли",
			ctx: Context{
				Base:   view | send,
				Roles:  []Overwrite{{Allow: send}},
				Member: &Overwrite{Deny: send},
			},
			want: view,
		},
		{
			name: "участник не может вернуть то, чего нет в базе",
			ctx: Context{
				Base:   view,
				Member: &Overwrite{Allow: send},
			},
			want: view | send,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := Compute(tt.ctx)
			if got != tt.want {
				t.Fatalf("Compute = %b, ожидали %b\n  не хватает: %b\n  лишние:     %b",
					got, tt.want, tt.want&^got, got&^tt.want)
			}
		})
	}
}

func TestHas(t *testing.T) {
	base := ViewChannel | SendMessages

	if !base.Has(ViewChannel) {
		t.Fatal("право ViewChannel должно быть")
	}
	if base.Has(Administrator) {
		t.Fatal("Administrator не должен быть")
	}
	if !Permission(Administrator).Has(ManageGuild) {
		t.Fatal("администратор должен иметь любое право")
	}
	if base.Has(ViewChannel|SendMessages) != true {
		t.Fatal("составное право должно проходить, когда есть оба бита")
	}
	if base.Has(ViewChannel | ManageGuild) {
		t.Fatal("составное право не должно проходить при нехватке одного бита")
	}
}

func TestWithWithout(t *testing.T) {
	p := ViewChannel
	p = p.With(SendMessages)
	if p != ViewChannel|SendMessages {
		t.Fatal("With не добавил право")
	}
	p = p.Without(SendMessages)
	if p != ViewChannel {
		t.Fatal("Without не снял право")
	}
}

func TestDefaultEveryoneContainsEssentials(t *testing.T) {
	for _, want := range []Permission{ViewChannel, SendMessages, Connect, Speak, ReadMessageHistory} {
		if !DefaultEveryone.Has(want) {
			t.Fatalf("в правах по умолчанию нет %b", want)
		}
	}
	if DefaultEveryone.Has(Administrator) {
		t.Fatal("Administrator не должен быть по умолчанию")
	}
	if DefaultEveryone.Has(ManageGuild) {
		t.Fatal("ManageGuild не должен быть по умолчанию")
	}
}
