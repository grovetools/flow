from calculator import add, subtract, multiply, divide

def main():
    print("Calculator Demo")
    x = 10
    y = 5
    
    print(f"{x} + {y} = {add(x, y)}")
    print(f"{x} - {y} = {subtract(x, y)}")
    print(f"{x} * {y} = {multiply(x, y)}")
    print(f"{x} / {y} = {divide(x, y)}")
    
    # Test division by zero
    print(f"{x} / 0 = {divide(x, 0)}")

if __name__ == "__main__":
    main()
