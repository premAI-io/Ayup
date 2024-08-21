import gradio as gr

from ultralytics import YOLOv10
from PIL import Image

print("Running YOLOv10 Gradio example", flush=True)

model = YOLOv10.from_pretrained('jameslahm/yolov10m')

def detect_objects(image):
    results = model.predict(source=image)

    detected_image_bgr = results[0].plot(probs=True)
    detected_image = Image.fromarray(detected_image_bgr[..., ::-1])

    print(f"Results count: {len(results)}", flush=True)

    return detected_image

iface = gr.Interface(
    fn=detect_objects,
    inputs=gr.Image(type="pil"),
    outputs=gr.Image(type="pil"),
    title="YOLOv10 Object Detection",
    description="Upload an image to detect objects using YOLOv10"
)

iface.launch(server_port=5000, server_name="0.0.0.0")
