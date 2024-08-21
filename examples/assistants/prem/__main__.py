import os
import sys

from mistletoe import Document
from mistletoe.block_token import CodeFence

from premai import Prem
from premai.models import ChatCompletionResponse, Message, MessageRoleEnum

# Ayup can read this from a .ayup-env file inside the assistant directory
# It's a regular dotenv file, but under a different name to avoid conflicts
prem_api_key = os.getenv("PREM_API_KEY")
if prem_api_key is None:
    raise ValueError("Environment variable PREM_API_KEY is not set. You can use a .ayup-env file to set it")

project_id = os.getenv("PREM_PROJECT_ID")
if project_id is None:
    raise ValueError("Environment variable PREM_PROJECT_ID is not set. You can set variables in .ayup-env")

# The application is written to /out/app which will be mounted as /app in the final container
out_app_path = '/out/app/__main__.py'

# The previous or existing application is available here
in_app_path = '/in/app/__main__.py'

# The logs for the previous execution are available here
in_log_path = '/in/log'

# The prompt file specifying what the app does. Taken from the source directory
in_spec_path= '/in/app/spec'

# The prompt file describing a correction or problem. Taken from the source directory
in_fix_path= '/in/app/fix'

client = Prem(api_key=prem_api_key)

def slurp(path: str) -> str:
    try:
        with open(path, 'r') as file:
            return file.read()
    except FileNotFoundError:
        return ''
    except Exception as e:
        raise Exception(f"Error reading '{path}': {e}")

messages: list[Message] = []

spec = slurp(in_spec_path)
src = slurp(in_app_path)
log = slurp(in_log_path)
fix = slurp(in_fix_path)

if src == '' and spec == '':
    raise Exception(f"Both {in_app_path} and {in_spec_path} are missing")

if fix == '' and spec == '':
    raise Exception(f"Both {in_fix_path} and {in_spec_path} are missing")

if spec != '':
    messages.append(Message(
        role=MessageRoleEnum.USER,
        content=spec
    ))

if src != '':
    messages.append(Message(
        role=MessageRoleEnum.ASSISTANT,
        content=f"""
```python
{src}
```
"""
    ))

    user_resp = ""
    if fix != '':
        user_resp += f"{fix}\n"
    if log != '':
        user_resp += f"""
When I ran the program and it produced the following log output:
```
{log}
```
"""
    messages.append(Message(
        role=MessageRoleEnum.USER,
        content=user_resp
    ))

for m in messages:
    print(f"\t{m.role}: ", m.content)

print("Generating code...")
response = client.chat.completions.create(
    project_id=int(project_id),
    system_prompt="""
You are a Python code generation assistant. The code you output inside markdown script tags like:
```python
# __main__.py
```
Will be concatenated together and written to a file called `__main__.py`. This file will be executed with `python __main__.py`.

The application must listen on port 5000 and bind to all addresses because it will run inside a container on Linux.
    """,
    messages = [x.to_dict() for x in messages],
    temperature=0
)
print(f"Prem response: {response}")

if not isinstance(response, ChatCompletionResponse):
    sys.exit(1)

c = response.choices[0].message.content
if not isinstance(c, str):
    print("No mesage content to write!")
    sys.exit(1)

# Parse the Markdown content
doc = Document(c)

# Function to recursively search for CodeFence tokens
def find_code_fences(token):
    if isinstance(token, CodeFence) and token.language == 'python':
        yield token.content
    if hasattr(token, 'children') and token.children is not None:
        for child in token.children:
            yield from find_code_fences(child)

# Extract Python code blocks
python_code_blocks = list(find_code_fences(doc))

with open(out_app_path, 'w') as file:
    for block in python_code_blocks:
        file.write(block)

print(f"File written to {out_app_path}")
