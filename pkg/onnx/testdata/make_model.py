#!/usr/bin/env python3
"""Build a small ONNX model shaped like the real tagging workload and record a
reference output, so the Go side can be checked for numeric agreement.

Shape mirrors vivid_galaxy's contract: NCHW float32 input with a DYNAMIC batch
dimension, multi-label sigmoid output over 10 classes.
"""
import json
import pathlib

import numpy as np
import onnx
import onnxruntime as ort
from onnx import TensorProto, helper, numpy_helper

OUT = pathlib.Path(__file__).parent
CLASSES = 10
SIDE = 64
CH = 8

rng = np.random.default_rng(1234)

w_conv = rng.standard_normal((CH, 3, 3, 3)).astype(np.float32) * 0.1
b_conv = np.zeros(CH, dtype=np.float32)
w_fc = rng.standard_normal((CH, CLASSES)).astype(np.float32) * 0.5
b_fc = np.zeros(CLASSES, dtype=np.float32)

graph = helper.make_graph(
    nodes=[
        helper.make_node("Conv", ["input", "w_conv", "b_conv"], ["conv"],
                         kernel_shape=[3, 3], pads=[1, 1, 1, 1]),
        helper.make_node("Relu", ["conv"], ["relu"]),
        helper.make_node("GlobalAveragePool", ["relu"], ["pooled"]),
        helper.make_node("Flatten", ["pooled"], ["flat"], axis=1),
        helper.make_node("Gemm", ["flat", "w_fc", "b_fc"], ["logits"]),
        helper.make_node("Sigmoid", ["logits"], ["output"]),
    ],
    name="tagging_head_spike",
    inputs=[helper.make_tensor_value_info(
        "input", TensorProto.FLOAT, ["batch", 3, SIDE, SIDE])],
    outputs=[helper.make_tensor_value_info(
        "output", TensorProto.FLOAT, ["batch", CLASSES])],
    initializer=[
        numpy_helper.from_array(w_conv, "w_conv"),
        numpy_helper.from_array(b_conv, "b_conv"),
        numpy_helper.from_array(w_fc, "w_fc"),
        numpy_helper.from_array(b_fc, "b_fc"),
    ],
)

model = helper.make_model(graph, opset_imports=[helper.make_opsetid("", 13)])
model.ir_version = 9  # ORT 1.28 / onnx 1.22 emit ir_version 10; pin to 9 for safety
onnx.checker.check_model(model)
onnx.save(model, OUT / "tagging_spike.onnx")

# Deterministic input the Go side reproduces exactly: a simple ramp.
BATCH = 4
n = BATCH * 3 * SIDE * SIDE
inp = (np.arange(n, dtype=np.float32) % 255.0) / 255.0
inp = inp.reshape(BATCH, 3, SIDE, SIDE)

sess = ort.InferenceSession(str(OUT / "tagging_spike.onnx"),
                            providers=["CPUExecutionProvider"])
ref = sess.run(["output"], {"input": inp})[0]

(OUT / "tagging_spike_reference.json").write_text(json.dumps({
    "batch": BATCH,
    "side": SIDE,
    "classes": CLASSES,
    "output": ref.astype(float).tolist(),
}, indent=2))

print(f"wrote tagging_spike.onnx  (opset 13, ir_version {model.ir_version})")
print(f"reference output {ref.shape}, first row: {ref[0][:4]}")
