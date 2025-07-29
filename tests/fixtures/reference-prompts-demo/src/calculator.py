def add(a, b):
    return a + b

def subtract(a, b):
    return a - b

def multiply(a, b):
    result = 0
    for i in range(b):
        result = add(result, a)
    return result

def divide(a, b):
    if b == 0:
        return "Error"
    return a / b
