// Дымовой тест: приложение собирается и показывает экран спайка.
// Никакой сети здесь нет — подключение к LiveKit проверяется вручную.

import 'package:flutter_test/flutter_test.dart';

import 'package:discord_clone_app/main.dart';

void main() {
  testWidgets('экран спайка отрисовывается', (WidgetTester tester) async {
    await tester.pumpWidget(const SpikeApp());

    expect(find.text('Спайк LiveKit'), findsOneWidget);
    expect(find.text('Подключиться'), findsOneWidget);
    expect(find.text('Нет видео'), findsOneWidget);
  });
}
