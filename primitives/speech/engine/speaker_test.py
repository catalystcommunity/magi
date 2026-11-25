"""Tests of the engine "speaker" (server.py) with the real models.

  MAGI_SPEAKER_TEST_MODEL_DIR=DIR MAGI_SPEAKER_TEST_WAVS=DIR \
    PYTHON -I primitives/speech/engine/speaker_test.py

MODEL_DIR is the model directory of the engine (provision.py makes it:
the speaker model, and voxlock/ for the extraction). WAVS has the test
files of the VoxLock model package (en-voice-sample-f.wav: one voice;
en-combined.wav: that voice with other voices). PYTHON is the Python of
the engine environment (requirements/speaker.txt). Without the two
variables, or without sherpa-onnx, the tests are skipped.
"""

import base64
import json
import os
import struct
import sys
import threading
import unittest
import urllib.error
import urllib.request
from http.server import ThreadingHTTPServer
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))
import server  # noqa: E402

MODELS = os.environ.get("MAGI_SPEAKER_TEST_MODEL_DIR", "")
WAVS = os.environ.get("MAGI_SPEAKER_TEST_WAVS", "")
try:
    import numpy as np
    import sherpa_onnx  # noqa: F401
    HAVE = bool(MODELS and WAVS)
except ImportError:
    HAVE = False
TOKEN = "t" * 32


def cosine(a, b):
    return float(np.dot(a, b))


@unittest.skipUnless(HAVE, "needs MAGI_SPEAKER_TEST_MODEL_DIR, MAGI_SPEAKER_TEST_WAVS, and sherpa-onnx")
class SpeakerEngine(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        d = Path(MODELS)
        pins = json.loads(Path(server.__file__).with_name("model-pins.json").read_text())
        server.verify(d, pins["speaker"]["sha256"])
        server.verify(d, {"voxlock/" + k: v for k, v in pins["voxlock"]["sha256"].items()})
        cls.run_ = staticmethod(server.load_speaker(d, 4))
        cls.voice = (Path(WAVS) / "en-voice-sample-f.wav").read_bytes()
        cls.mix = (Path(WAVS) / "en-combined.wav").read_bytes()
        srv = ThreadingHTTPServer(("127.0.0.1", 0), server.Handler)
        srv.daemon_threads = True
        srv.kind, srv.engine_name, srv.run, srv.token = "speaker", server.SPEAKER_MODEL + "+voxlock", cls.run_, TOKEN
        srv.lock, srv.device = threading.Lock(), "cpu"
        threading.Thread(target=srv.serve_forever, daemon=True).start()
        cls.srv = srv
        cls.url = f"http://127.0.0.1:{srv.server_address[1]}"

    @classmethod
    def tearDownClass(cls):
        cls.srv.shutdown()

    def post(self, body, token=TOKEN):
        req = urllib.request.Request(self.url + "/speaker", data=json.dumps(body).encode(), method="POST",
                                     headers={"Content-Type": "application/json", "Authorization": "Bearer " + token})
        with urllib.request.urlopen(req, timeout=120) as r:
            return json.loads(r.read())

    def test_print_has_unit_length(self):
        v = self.run_(server.decode_audio(self.voice), None)["vector"]
        self.assertEqual(v.shape, (192,))
        self.assertAlmostEqual(float(np.linalg.norm(v)), 1.0, places=4)

    def test_too_little_sound_gives_no_print(self):
        quiet = np.zeros(16000, dtype="float32")
        self.assertIsNone(self.run_(quiet, None)["vector"])
        short = (np.sin(np.arange(3200, dtype="float32") / 20) * 0.2).astype("float32")
        self.assertIsNone(self.run_(short, None)["vector"])

    def test_extraction_brings_the_voice_back(self):
        voice = server.decode_audio(self.voice)
        mix = server.decode_audio(self.mix)
        want = self.run_(voice, None)["vector"]
        res = self.run_(mix, voice)
        before, after = cosine(want, res["vector"]), cosine(want, res["extracted_vector"])
        self.assertIsNotNone(res["extracted"])
        self.assertAlmostEqual(res["extracted"].size / mix.size, 1.0, delta=0.05)
        # The extracted audio is nearer to the voice of the sample than the
        # mixture is, and it is over the accept limit of Magi (0.40).
        self.assertGreater(after, before)
        self.assertGreater(after, 0.40)

    def test_no_sample_gives_no_extraction(self):
        res = self.run_(server.decode_audio(self.mix), None)
        self.assertIsNone(res["extracted"])
        self.assertIsNone(res["extracted_vector"])

    def test_short_sample_gives_no_extraction(self):
        # VoxLock ignores a sample under 3 seconds and keeps the loudest
        # voice of the audio. The engine must not run it then.
        voice = server.decode_audio(self.voice)
        res = self.run_(server.decode_audio(self.mix), voice[:2 * 16000])
        self.assertIsNone(res["extracted"])

    def test_short_audio_is_extracted(self):
        voice = server.decode_audio(self.voice)
        mix = server.decode_audio(self.mix)[4 * 16000:6 * 16000]
        want = self.run_(voice, None)["vector"]
        res = self.run_(mix, voice)
        self.assertIsNotNone(res["extracted"])
        self.assertGreater(cosine(want, res["extracted_vector"]), cosine(want, res["vector"]))

    def test_http(self):
        b64 = lambda b: base64.b64encode(b).decode()
        out = self.post({"audio": b64(self.voice)})
        self.assertEqual(out["model"], server.SPEAKER_MODEL)
        self.assertNotIn("extracted", out)
        v = base64.b64decode(out["vector"])
        self.assertEqual(len(v), 192 * 4)
        self.assertAlmostEqual(sum(x * x for x in struct.unpack("<192f", v)), 1.0, places=3)
        out = self.post({"audio": b64(self.mix), "sample": b64(self.voice)})
        self.assertEqual(base64.b64decode(out["extracted"])[:4], b"RIFF")
        self.assertEqual(len(base64.b64decode(out["extracted_vector"])), 192 * 4)
        with self.assertRaises(urllib.error.HTTPError) as e:
            self.post({"audio": b64(self.voice)}, token="wrong" * 8)
        self.assertEqual(e.exception.code, 401)
        with self.assertRaises(urllib.error.HTTPError) as e:
            self.post({"sample": b64(self.voice)})
        self.assertEqual(e.exception.code, 400)


if __name__ == "__main__":
    unittest.main()
