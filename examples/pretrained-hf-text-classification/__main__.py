from flask import Flask, request, jsonify
from transformers import AutoTokenizer, AutoModelForSequenceClassification
import torch

app = Flask(__name__)

# Load the model and tokenizer
tokenizer = AutoTokenizer.from_pretrained("mrm8488/distilroberta-finetuned-financial-news-sentiment-analysis")
model = AutoModelForSequenceClassification.from_pretrained("mrm8488/distilroberta-finetuned-financial-news-sentiment-analysis")

@app.route('/classify', methods=['POST'])
def classify_text():
    data = request.json
    if not data:
        return jsonify({'error': 'No JSON in POST body'}), 400

    text = data.get('text', '')
    if not text:
        return jsonify({'error': 'No text provided'}), 400

    # Tokenize the input text
    inputs = tokenizer(text, return_tensors='pt')

    # Get model predictions
    with torch.no_grad():
        outputs = model(**inputs)

    # Get the predicted class
    predictions = torch.nn.functional.softmax(outputs.logits, dim=-1)
    predicted_class = torch.argmax(predictions, dim=-1).item()

    return jsonify({'class': predicted_class, 'predictions': predictions.tolist()})

if __name__ == '__main__':
    app.run(host='0.0.0.0')
