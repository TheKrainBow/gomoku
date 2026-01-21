#ifndef COORDINATEMAPPER_HPP
#define COORDINATEMAPPER_HPP

#include "UiLayout.hpp"

class CoordinateMapper {
public:
	explicit CoordinateMapper(const UiLayout& layout);
	bool pixelToCell(int px, int py, int& outX, int& outY) const;
	void cellToPixelCenter(int x, int y, int& outPx, int& outPy) const;

private:
	UiLayout layout;
};

#endif
