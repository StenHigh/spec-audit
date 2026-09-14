<?php

declare(strict_types=1);

final class SubjectTest extends PHPUnit\Framework\TestCase
{
    public function testLeadingZeroes(): void
    {
        $this->assertEquals('0099', (new Subject)->answer());
    }
}
