// Спайк LiveKit.
//
// Цель — ответить на один вопрос: работает ли livekit_client на нашей версии
// Flutter и видит ли он камеру и микрофон на эмуляторе.
//
// Это НЕ часть продукта. После спайка этот экран заменится нормальным UI.
//
// Запуск:
//   $token = .\scripts\dev-token.ps1
//   flutter run --dart-define=LK_TOKEN=$token
//
// Адрес по умолчанию ws://10.0.2.2:7880 — это localhost компьютера
// с точки зрения Android-эмулятора.

import 'package:flutter/material.dart';
import 'package:livekit_client/livekit_client.dart';

const String kLiveKitUrl = String.fromEnvironment(
  'LK_URL',
  defaultValue: 'ws://10.0.2.2:7880',
);

const String kLiveKitToken = String.fromEnvironment('LK_TOKEN');

// Для автоматической проверки без клика: --dart-define=LK_AUTOCONNECT=true
const bool kAutoConnect = bool.fromEnvironment('LK_AUTOCONNECT');

void main() {
  runApp(const SpikeApp());
}

class SpikeApp extends StatelessWidget {
  const SpikeApp({super.key});

  @override
  Widget build(BuildContext context) {
    return MaterialApp(
      title: 'LiveKit spike',
      debugShowCheckedModeBanner: false,
      theme: ThemeData(colorSchemeSeed: Colors.indigo, useMaterial3: true),
      home: const SpikePage(),
    );
  }
}

class SpikePage extends StatefulWidget {
  const SpikePage({super.key});

  @override
  State<SpikePage> createState() => _SpikePageState();
}

class _SpikePageState extends State<SpikePage> {
  Room? _room;
  VideoTrack? _localVideo;
  String _status = 'Не подключено';
  bool _busy = false;
  bool _cameraOn = false;

  @override
  void initState() {
    super.initState();
    if (kAutoConnect) {
      WidgetsBinding.instance.addPostFrameCallback((_) => _connect());
    }
  }

  @override
  void dispose() {
    _room?.removeListener(_onRoomChanged);
    _room?.disconnect();
    super.dispose();
  }

  void _onRoomChanged() {
    if (!mounted) return;
    setState(() {
      _status = 'В комнате участников: ${_room!.remoteParticipants.length + 1}';
    });
  }

  Future<void> _connect() async {
    if (kLiveKitToken.isEmpty) {
      setState(() => _status =
          'Токен не задан.\nСоберите с --dart-define=LK_TOKEN=<токен>');
      return;
    }

    setState(() {
      _busy = true;
      _status = 'Подключаюсь к $kLiveKitUrl ...';
    });

    try {
      final room = Room();
      room.addListener(_onRoomChanged);

      await room.connect(kLiveKitUrl, kLiveKitToken);
      await room.localParticipant?.setMicrophoneEnabled(true);
      await room.localParticipant?.setCameraEnabled(true);

      // Даём движку время выдать кадры первого трека.
      await Future<void>.delayed(const Duration(milliseconds: 800));

      VideoTrack? video;
      final publications = room.localParticipant?.videoTrackPublications ?? [];
      for (final publication in publications) {
        final track = publication.track;
        if (track is VideoTrack) {
          video = track;
          break;
        }
      }

      setState(() {
        _room = room;
        _localVideo = video;
        _cameraOn = video != null;
        _status = video == null
            ? 'Подключено, но камера не дала трек'
            : 'Подключено. Микрофон и камера включены';
      });
    } catch (error) {
      setState(() {
        _status = 'Ошибка: $error';
        _room = null;
        _localVideo = null;
      });
    } finally {
      if (mounted) setState(() => _busy = false);
    }
  }

  Future<void> _disconnect() async {
    await _room?.disconnect();
    _room?.removeListener(_onRoomChanged);
    setState(() {
      _room = null;
      _localVideo = null;
      _cameraOn = false;
      _status = 'Отключено';
    });
  }

  Future<void> _toggleCamera() async {
    final participant = _room?.localParticipant;
    if (participant == null) return;

    final enable = !_cameraOn;
    await participant.setCameraEnabled(enable);
    await Future<void>.delayed(const Duration(milliseconds: 400));

    VideoTrack? video;
    if (enable) {
      for (final publication in participant.videoTrackPublications) {
        final track = publication.track;
        if (track is VideoTrack) {
          video = track;
          break;
        }
      }
    }

    setState(() {
      _cameraOn = enable;
      _localVideo = video;
    });
  }

  @override
  Widget build(BuildContext context) {
    final connected = _room != null;

    return Scaffold(
      appBar: AppBar(title: const Text('Спайк LiveKit')),
      body: Padding(
        padding: const EdgeInsets.all(16),
        child: Column(
          crossAxisAlignment: CrossAxisAlignment.stretch,
          children: [
            Container(
              padding: const EdgeInsets.all(12),
              decoration: BoxDecoration(
                color: Theme.of(context).colorScheme.surfaceContainerHighest,
                borderRadius: BorderRadius.circular(8),
              ),
              child: Text(_status),
            ),
            const SizedBox(height: 16),
            Expanded(
              child: Container(
                decoration: BoxDecoration(
                  color: Colors.black87,
                  borderRadius: BorderRadius.circular(12),
                ),
                clipBehavior: Clip.antiAlias,
                child: _localVideo != null
                    ? VideoTrackRenderer(_localVideo!)
                    : Center(
                        child: Text(
                          connected ? 'Камера выключена' : 'Нет видео',
                          style: const TextStyle(color: Colors.white70),
                        ),
                      ),
              ),
            ),
            const SizedBox(height: 16),
            Wrap(
              spacing: 12,
              runSpacing: 12,
              alignment: WrapAlignment.center,
              children: [
                FilledButton.icon(
                  onPressed: _busy || connected ? null : _connect,
                  icon: const Icon(Icons.login),
                  label: const Text('Подключиться'),
                ),
                OutlinedButton.icon(
                  onPressed: connected ? _toggleCamera : null,
                  icon: Icon(_cameraOn ? Icons.videocam_off : Icons.videocam),
                  label: Text(_cameraOn ? 'Выключить камеру' : 'Включить камеру'),
                ),
                OutlinedButton.icon(
                  onPressed: connected ? _disconnect : null,
                  icon: const Icon(Icons.logout),
                  label: const Text('Отключиться'),
                ),
              ],
            ),
            const SizedBox(height: 8),
            Text(
              'Сервер: $kLiveKitUrl',
              textAlign: TextAlign.center,
              style: Theme.of(context).textTheme.bodySmall,
            ),
          ],
        ),
      ),
    );
  }
}
