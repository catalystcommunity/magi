import argparse
from functools import partial
from http.server import ThreadingHTTPServer
import json
from pathlib import Path
import tempfile
import threading
import unittest
from unittest.mock import patch
import urllib.error
import urllib.request
import wave

import numpy as np

import audition
import listen
import verify


class AuditionTests(unittest.TestCase):
    def setUp(self):
        self.temporary = tempfile.TemporaryDirectory()
        self.root = Path(self.temporary.name)

    def tearDown(self):
        self.temporary.cleanup()

    def test_pcm_preserves_amplitude_and_records_limits(self):
        path = self.root / "test.wav"
        result = audition.encode_audio([np.array([-1.2, 0, .25, 1.2])], 24000, path)
        self.assertEqual(result["clipped_samples"], 2)
        self.assertEqual(result["frames"], 4)
        with wave.open(str(path)) as source:
            self.assertEqual(source.getnchannels(), 1)
            self.assertEqual(source.getframerate(), 24000)
            np.testing.assert_array_equal(np.frombuffer(source.readframes(4), dtype="<i2"),
                                          [-32768, 0, 8192, 32767])

    def test_rejects_empty_nonfinite_stereo_and_long_audio(self):
        for chunks in ([], [np.array([])], [np.array([float("nan")])],
                       [np.array([float("inf")])], [np.zeros((2, 2))], [np.zeros(121)]):
            with self.subTest(chunks=chunks), self.assertRaises(ValueError):
                audition.encode_audio(chunks, 1, self.root / "bad.wav")

    def test_measures_first_yield_and_each_chunk(self):
        class Fake:
            def generate(self, text, voice):
                yield np.ones(240), 24000
                yield np.zeros(240), 24000
        result = audition.measure(Fake(), "test", "test", self.root / "chunks.wav")
        self.assertEqual(result["audio_seconds"], .02)
        self.assertEqual(len(result["chunks"]), 2)
        self.assertLessEqual(result["first_audio_seconds"], result["generation_seconds"])

    def test_rejects_changing_sample_rate(self):
        class Fake:
            def generate(self, text, voice):
                yield np.zeros(10), 24000
                yield np.zeros(10), 16000
        with self.assertRaises(ValueError):
            audition.measure(Fake(), "test", "test", self.root / "bad.wav")

    def test_rejects_prompt_paths_and_duplicate_ids(self):
        for prompts in ([{"id": "../outside", "text": "test"}],
                        [{"id": "a", "text": "test"}, {"id": "a", "text": "two"}]):
            path = self.root / "prompts.json"
            audition.save_json(path, {"prompts": prompts})
            with self.assertRaises(ValueError):
                audition.read_prompts(path)

    def test_repo_prompts_and_voice_catalogues(self):
        prompts = audition.read_prompts(Path(audition.__file__).with_name("prompts.json"))
        self.assertEqual(len(prompts), 5)
        for spec in audition.MODELS.values():
            self.assertGreaterEqual(len(set(spec["voices"])), 8)
            self.assertEqual(len(spec["revision"]), 40)

    def test_failed_model_load_retains_manifest(self):
        models = self.root / "models"
        models.mkdir()
        audition.save_json(models / "kokoro-provenance.json", {
            "revision": audition.MODELS["kokoro"]["revision"], "sha256": {}})
        args = argparse.Namespace(engine="kokoro", models=models, output=self.root / "run",
                                  prompts=Path(audition.__file__).with_name("prompts.json"),
                                  prompt_ids=None, voices=None, threads=1, repeats=1, seed=1729)
        with patch.object(audition, "Kokoro", side_effect=RuntimeError("fixture load failure")):
            with self.assertRaisesRegex(RuntimeError, "fixture load failure"):
                audition.run(args)
        report = json.loads((args.output / "manifest.json").read_text())
        self.assertEqual(report["status"], "failed")
        self.assertEqual(report["samples"], [])
        with self.assertRaises(FileExistsError):
            audition.run(args)

    def test_verifier_rejects_changed_or_missing_audio(self):
        run = self.root / "results" / "fixture"
        run.mkdir(parents=True)
        class Fake:
            def generate(self, text, voice):
                yield np.ones(240, dtype=np.float32) * .1, 24000
        sample = {"voice": "stock", "prompt": "ack", "repeat": 1, "file": "test.wav"}
        sample.update(audition.measure(Fake(), "test", "stock", run / "test.wav"))
        record = {"status": "complete", "engine": "fixture", "voices": ["stock"],
                  "prompts": [{"id": "ack"}], "repeats": 1, "samples": [sample],
                  "model_load_seconds": .1, "peak_rss_kib": 1024}
        audition.save_json(run / "manifest.json", record)
        summary = verify.verify(self.root / "results")
        self.assertEqual(summary["runs"][0]["samples"], 1)
        original = (run / "test.wav").read_bytes()
        (run / "test.wav").write_bytes(b"changed")
        with self.assertRaisesRegex(ValueError, "hash mismatch"):
            verify.verify(self.root / "results")
        (run / "test.wav").write_bytes(original)
        record["samples"] = []
        audition.save_json(run / "manifest.json", record)
        with self.assertRaisesRegex(ValueError, "Missing or duplicate"):
            verify.verify(self.root / "results")
        record["status"] = "running"
        audition.save_json(run / "manifest.json", record)
        with self.assertRaisesRegex(ValueError, "Incomplete run"):
            verify.verify(self.root / "results")


class ListeningTests(unittest.TestCase):
    def test_catalogue_audio_and_path_confinement(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory) / "results"
            run = root / "run"
            run.mkdir(parents=True)
            audition.save_json(run / "manifest.json", {
                "engine": "fake", "status": "complete", "prompts": [], "samples": [],
                "packages": {"private-inventory": "not-sent"},
            })
            (run / "sample.wav").write_bytes(b"RIFF-test")
            outside = Path(directory) / "outside.wav"
            outside.write_bytes(b"outside")
            (run / "escape.wav").symlink_to(outside)
            server = ThreadingHTTPServer(("127.0.0.1", 0), partial(listen.Handler, results=root))
            worker = threading.Thread(target=server.serve_forever, daemon=True)
            worker.start()
            base = f"http://127.0.0.1:{server.server_port}"
            try:
                with urllib.request.urlopen(base + "/catalog.json") as response:
                    data = json.load(response)
                    self.assertEqual(data["runs"][0]["engine"], "fake")
                    self.assertNotIn("packages", data["runs"][0])
                with urllib.request.urlopen(base + "/audio/run/sample.wav") as response:
                    self.assertEqual(response.read(), b"RIFF-test")
                    self.assertEqual(response.headers["Content-Type"], "audio/wav")
                for path in ("/audio/run/escape.wav", "/audio/run/manifest.json",
                             "/audio/%2e%2e/outside.wav", "/requirements.lock"):
                    with self.subTest(path=path), self.assertRaises(urllib.error.HTTPError) as error:
                        urllib.request.urlopen(base + path)
                    self.assertEqual(error.exception.code, 404)
            finally:
                server.shutdown()
                server.server_close()
                worker.join()


if __name__ == "__main__":
    unittest.main()
