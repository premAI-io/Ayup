file_path = '/out/app/__main__.py'
file_content = """from flask import Flask

app = Flask(__name__)

@app.route('/')
def hello_world():
    return 'Hello, World!'

if __name__ == '__main__':
    app.run(debug=True, host="0.0.0.0")
"""

with open(file_path, 'w') as file:
    file.write(file_content)

print(f"File written to {file_path}")
